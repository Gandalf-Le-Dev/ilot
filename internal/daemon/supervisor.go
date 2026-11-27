package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"sync"
	"syscall"
	"time"
)

var (
	ErrUnknownProcess = errors.New("the daemon has no process of that name")
	ErrShuttingDown   = errors.New("the daemon shuts down")
)

const (
	defaultStopTimeout = 20 * time.Second
	// A grandchild that left the process group can hold the output pipe open for ever.
	outputDrainTimeout = 2 * time.Second
	exitCodeNotStarted = -1
)

type SupervisorOptions struct {
	Logger      *slog.Logger
	StopTimeout time.Duration
	LogMaxBytes int64
	// The time between two readiness probes, and the longest time of one probe.
	ProbeInterval time.Duration
	ProbeTimeout  time.Duration
}

type Supervisor struct {
	logger        *slog.Logger
	stopTimeout   time.Duration
	logMaxBytes   int64
	probeInterval time.Duration
	probeTimeout  time.Duration

	mu         sync.Mutex
	entries    map[string]*entry
	isClosed   bool
	background sync.WaitGroup
}

type entry struct {
	// Serializes the start, the restart and the removal of one name, so that a slow stop blocks no other process.
	operation sync.Mutex

	// The fields below are guarded by Supervisor.mu.
	spec      Spec
	hash      string
	run       *run
	isRemoved bool
}

type run struct {
	pid    int
	output *rotatingFile
	// Cancelled when the process has exited and its output is in the log file.
	exited context.Context
	// Closed at the first probe that passes, or at the start when the specification has no probe.
	ready chan struct{}

	// Guarded by Supervisor.mu.
	state    State
	exitCode int
}

func NewSupervisor(options SupervisorOptions) *Supervisor {
	return &Supervisor{
		logger:        cmp.Or(options.Logger, slog.New(slog.DiscardHandler)),
		stopTimeout:   cmp.Or(options.StopTimeout, defaultStopTimeout),
		logMaxBytes:   cmp.Or(options.LogMaxBytes, defaultLogMaxBytes),
		probeInterval: cmp.Or(options.ProbeInterval, defaultProbeInterval),
		probeTimeout:  cmp.Or(options.ProbeTimeout, defaultProbeTimeout),
		entries:       map[string]*entry{},
	}
}

// Put starts the process. A live process with the same specification stays as it is.
func (supervisor *Supervisor) Put(name string, spec Spec) (Process, error) {
	if name == "" {
		return Process{}, errors.New("the process has no name")
	}
	if err := spec.Validate(); err != nil {
		return Process{}, fmt.Errorf("%s: %w", name, err)
	}
	target, err := supervisor.lockEntry(name, true)
	if err != nil {
		return Process{}, err
	}
	defer target.operation.Unlock()

	hash := spec.Hash()
	supervisor.mu.Lock()
	isUnchanged := target.run != nil && target.run.state != StateExited && target.hash == hash
	supervisor.mu.Unlock()
	if isUnchanged {
		return supervisor.describe(name, target), nil
	}
	supervisor.stop(name, target)
	supervisor.mu.Lock()
	target.spec, target.hash, target.run = spec, hash, nil
	supervisor.mu.Unlock()
	err = supervisor.start(name, target)
	return supervisor.describe(name, target), err
}

func (supervisor *Supervisor) Restart(name string) (Process, error) {
	target, err := supervisor.lockEntry(name, false)
	if err != nil {
		return Process{}, err
	}
	defer target.operation.Unlock()
	if supervisor.describe(name, target).Hash == "" {
		// A Put created the entry and did not fill it yet.
		return Process{}, fmt.Errorf("%s: %w", name, ErrUnknownProcess)
	}
	supervisor.stop(name, target)
	err = supervisor.start(name, target)
	return supervisor.describe(name, target), err
}

// Delete stops the process and forgets it. An unknown name is not an error, so that a stop can run twice.
func (supervisor *Supervisor) Delete(name string) error {
	target, err := supervisor.lockEntry(name, false)
	if errors.Is(err, ErrUnknownProcess) {
		return nil
	}
	if err != nil {
		return err
	}
	defer target.operation.Unlock()
	supervisor.stop(name, target)
	supervisor.mu.Lock()
	target.isRemoved = true
	delete(supervisor.entries, name)
	supervisor.mu.Unlock()
	return nil
}

func (supervisor *Supervisor) List() []Process {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	processes := make([]Process, 0, len(supervisor.entries))
	for name, target := range supervisor.entries {
		processes = append(processes, describeLocked(name, target))
	}
	slices.SortFunc(processes, func(left, right Process) int { return cmp.Compare(left.Name, right.Name) })
	return processes
}

// Shutdown stops every process and refuses every later start.
func (supervisor *Supervisor) Shutdown() {
	supervisor.mu.Lock()
	supervisor.isClosed = true
	targets := make(map[string]*entry, len(supervisor.entries))
	for name, target := range supervisor.entries {
		targets[name] = target
	}
	supervisor.mu.Unlock()

	var stops sync.WaitGroup
	for name, target := range targets {
		stops.Go(func() {
			target.operation.Lock()
			defer target.operation.Unlock()
			supervisor.stop(name, target)
		})
	}
	stops.Wait()
	supervisor.background.Wait()
}

func (supervisor *Supervisor) lockEntry(name string, createIfMissing bool) (*entry, error) {
	for {
		supervisor.mu.Lock()
		if supervisor.isClosed {
			supervisor.mu.Unlock()
			return nil, ErrShuttingDown
		}
		target, isKnown := supervisor.entries[name]
		if !isKnown {
			if !createIfMissing {
				supervisor.mu.Unlock()
				return nil, fmt.Errorf("%s: %w", name, ErrUnknownProcess)
			}
			target = &entry{}
			supervisor.entries[name] = target
		}
		supervisor.mu.Unlock()

		target.operation.Lock()
		supervisor.mu.Lock()
		isRemoved := target.isRemoved
		supervisor.mu.Unlock()
		if !isRemoved {
			return target, nil
		}
		target.operation.Unlock()
	}
}

func (supervisor *Supervisor) describe(name string, target *entry) Process {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	return describeLocked(name, target)
}

func describeLocked(name string, target *entry) Process {
	process := Process{Name: name, Hash: target.hash, State: StateExited, ExitCode: exitCodeNotStarted}
	if target.run == nil {
		return process
	}
	process.State = target.run.state
	process.ExitCode = target.run.exitCode
	if target.run.state != StateExited {
		process.PID = target.run.pid
	}
	return process
}

func (supervisor *Supervisor) start(name string, target *entry) error {
	supervisor.mu.Lock()
	spec := target.spec
	supervisor.mu.Unlock()

	output, err := openRotatingFile(spec.LogFile, supervisor.logMaxBytes)
	if err != nil {
		return fmt.Errorf("open the log file of %s: %w", name, err)
	}
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		output.Close()
		return fmt.Errorf("start %s: %w", name, err)
	}
	command := shellCommand(context.Background(), spec, spec.Command)
	// The pipe is ours and not the one of os/exec, so that Wait returns when the shell exits, even if a grandchild keeps the pipe open.
	command.Stdout, command.Stderr = pipeWriter, pipeWriter

	// The closed test and the start share one critical section: a start after Shutdown would leave a process behind.
	supervisor.mu.Lock()
	if supervisor.isClosed {
		supervisor.mu.Unlock()
		pipeReader.Close()
		pipeWriter.Close()
		output.Close()
		return ErrShuttingDown
	}
	err = command.Start()
	pipeWriter.Close()
	if err != nil {
		supervisor.mu.Unlock()
		pipeReader.Close()
		output.Close()
		return fmt.Errorf("start %s: %w", name, err)
	}
	exited, markExited := context.WithCancel(context.Background())
	started := &run{pid: command.Process.Pid, output: output, exited: exited, ready: make(chan struct{}), state: StateStarting}
	if spec.Ready == nil {
		started.state = StateReady
		close(started.ready)
	} else {
		supervisor.background.Go(func() { supervisor.probeUntilReady(name, spec, started) })
	}
	target.run = started
	supervisor.background.Go(func() {
		supervisor.watch(name, started, command, pipeReader, markExited)
	})
	supervisor.mu.Unlock()

	supervisor.logger.Info("process started", "process", name, "pid", started.pid)
	return nil
}

func (supervisor *Supervisor) watch(name string, watched *run, command *exec.Cmd, pipeReader *os.File, markExited context.CancelFunc) {
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		if _, err := io.Copy(watched.output, pipeReader); err != nil {
			supervisor.logger.Error("the output of the process is lost", "process", name, "error", err)
			// The pipe must drain even with no log file: a full pipe would block the process.
			_, _ = io.Copy(io.Discard, pipeReader)
		}
	}()

	_ = command.Wait()
	// Whatever the shell left in its group must not outlive it.
	_ = syscall.Kill(-watched.pid, syscall.SIGKILL)
	select {
	case <-copied:
	case <-time.After(outputDrainTimeout):
	}
	pipeReader.Close()
	<-copied
	watched.output.Close()

	exitCode := exitCodeOf(command.ProcessState)
	supervisor.mu.Lock()
	watched.state, watched.exitCode = StateExited, exitCode
	supervisor.mu.Unlock()
	markExited()
	supervisor.logger.Info("process exited", "process", name, "pid", watched.pid, "exit_code", exitCode)
}

func exitCodeOf(processState *os.ProcessState) int {
	if status, isWaitStatus := processState.Sys().(syscall.WaitStatus); isWaitStatus && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return processState.ExitCode()
}

func (supervisor *Supervisor) stop(name string, target *entry) {
	supervisor.mu.Lock()
	stopped, spec := target.run, target.spec
	if stopped == nil || stopped.state == StateExited {
		supervisor.mu.Unlock()
		return
	}
	stopped.state = StateStopping
	supervisor.mu.Unlock()

	deadline, cancel := context.WithTimeout(context.Background(), supervisor.stopTimeout)
	defer cancel()
	supervisor.logger.Info("process stops", "process", name, "pid", stopped.pid)
	if spec.Stop == "" || !supervisor.runStopCommand(deadline, name, spec, stopped) {
		_ = syscall.Kill(-stopped.pid, syscall.SIGTERM)
	}
	select {
	case <-stopped.exited.Done():
		return
	case <-deadline.Done():
	}
	supervisor.logger.Warn("process killed, because it did not stop in time", "process", name, "pid", stopped.pid, "timeout", supervisor.stopTimeout.String())
	_ = syscall.Kill(-stopped.pid, syscall.SIGKILL)
	<-stopped.exited.Done()
}

func (supervisor *Supervisor) runStopCommand(deadline context.Context, name string, spec Spec, stopped *run) (hasSucceeded bool) {
	command := shellCommand(deadline, spec, spec.Stop)
	command.Stdout, command.Stderr = stopped.output, stopped.output
	if err := command.Run(); err != nil {
		supervisor.logger.Warn("the stop command failed, the process gets SIGTERM", "process", name, "error", err)
		return false
	}
	return true
}

// shellCommand runs in its own process group, and a cancel kills the whole group.
func shellCommand(ctx context.Context, spec Spec, script string) *exec.Cmd {
	command := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	command.Dir = spec.Dir
	// A nil slice would give the process the environment of the daemon.
	command.Env = make([]string, 0, len(spec.Env))
	for key, value := range spec.Env {
		command.Env = append(command.Env, key+"="+value)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second
	return command
}
