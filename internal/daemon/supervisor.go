package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
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
	// A command with a mistake exits within this time, and must not look ready to a caller that waits.
	defaultAliveWindow = 500 * time.Millisecond
)

type SupervisorOptions struct {
	Logger      *slog.Logger
	StopTimeout time.Duration
	LogMaxBytes int64
	// The time between two readiness probes, and the longest time of one probe.
	ProbeInterval time.Duration
	ProbeTimeout  time.Duration
	// How long a process with no probe must live before it is ready.
	AliveWindow time.Duration
	// Where the supervisor records its live processes, for the daemon that comes after a crash. Empty: no record.
	RecordFile string
}

type Supervisor struct {
	logger        *slog.Logger
	stopTimeout   time.Duration
	logMaxBytes   int64
	probeInterval time.Duration
	probeTimeout  time.Duration
	aliveWindow   time.Duration
	recordFile    string
	recordMu      sync.Mutex
	bootID        string
	session       int
	// A test replaces it to end the alive window at a chosen moment.
	afterAliveWindow func(time.Duration) <-chan time.Time

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
	pid int
	// Tells this process from a later one with the same ID.
	startedAt string
	output    *rotatingFile
	// Cancelled when the process has exited, before its output is read to the end: a grandchild can hold that output open.
	dead context.Context
	// Cancelled when the process has exited and its output is in the log file.
	exited context.Context
	// Closed at the first probe that passes, or after the alive window when the specification has no probe.
	ready chan struct{}

	// Guarded by Supervisor.mu.
	state    State
	exitCode int
}

func NewSupervisor(options SupervisorOptions) *Supervisor {
	session, _ := sessionOf(os.Getpid())
	return &Supervisor{
		bootID:        bootID(),
		session:       session,
		logger:        cmp.Or(options.Logger, slog.New(slog.DiscardHandler)),
		stopTimeout:   cmp.Or(options.StopTimeout, defaultStopTimeout),
		logMaxBytes:   cmp.Or(options.LogMaxBytes, defaultLogMaxBytes),
		probeInterval: cmp.Or(options.ProbeInterval, defaultProbeInterval),
		probeTimeout:  cmp.Or(options.ProbeTimeout, defaultProbeTimeout),
		aliveWindow:   cmp.Or(options.AliveWindow, defaultAliveWindow),
		recordFile:    options.RecordFile,
		entries:       map[string]*entry{},

		afterAliveWindow: time.After,
	}
}

// Put starts the process. A live process with the same specification stays as it is.
func (supervisor *Supervisor) Put(name string, spec Spec) (Process, error) {
	if err := validateName(name); err != nil {
		return Process{}, err
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
	return supervisor.restart(name, nil)
}

// RestartWith restarts a known process with a specification that takes the place of the stored one, even with the same hash:
// the specification brings the session of the terminal that asks, for example a new SSH_AUTH_SOCK.
func (supervisor *Supervisor) RestartWith(name string, spec Spec) (Process, error) {
	if err := spec.Validate(); err != nil {
		return Process{}, fmt.Errorf("%s: %w", name, err)
	}
	return supervisor.restart(name, &spec)
}

func (supervisor *Supervisor) restart(name string, replacement *Spec) (Process, error) {
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
	if replacement != nil {
		supervisor.mu.Lock()
		target.spec, target.hash, target.run = *replacement, replacement.Hash(), nil
		supervisor.mu.Unlock()
	}
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

func (supervisor *Supervisor) IsShuttingDown() bool {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	return supervisor.isClosed
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
	output.onRotationFailure = func(err error) {
		supervisor.logger.Error("the log file does not rotate, so it grows", "process", name, "file", spec.LogFile, "error", err)
	}
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		output.Close()
		return fmt.Errorf("start %s: %w", name, err)
	}
	command := shellCommand(context.Background(), spec, spec.Command)
	// The pipe is ours and not the one of os/exec, so that Wait returns when the shell exits, even if a grandchild keeps the pipe open.
	command.Stdout, command.Stderr = pipeWriter, pipeWriter

	// Shutdown may have passed this entry already. After this test it must wait for the operation lock that the caller holds.
	supervisor.mu.Lock()
	isClosed := supervisor.isClosed
	supervisor.mu.Unlock()
	if !isClosed {
		err = command.Start()
	}
	pipeWriter.Close()
	if isClosed || err != nil {
		pipeReader.Close()
		output.Close()
		if isClosed {
			return ErrShuttingDown
		}
		return fmt.Errorf("start %s: %w", name, err)
	}
	dead, markDead := context.WithCancel(context.Background())
	exited, markExited := context.WithCancel(context.Background())
	started := &run{pid: command.Process.Pid, output: output, dead: dead, exited: exited, ready: make(chan struct{}), state: StateStarting}
	started.startedAt, _ = processStartTime(started.pid)
	supervisor.mu.Lock()
	target.run = started
	supervisor.mu.Unlock()
	supervisor.saveRecord()
	if spec.Ready != nil {
		supervisor.background.Go(func() { supervisor.probeUntilReady(name, spec, started) })
	} else {
		supervisor.background.Go(func() { supervisor.readyIfStillAlive(name, started) })
	}
	supervisor.background.Go(func() {
		supervisor.watch(name, started, command, pipeReader, markDead, markExited)
	})

	supervisor.logger.Info("process started", "process", name, "pid", started.pid)
	return nil
}

func (supervisor *Supervisor) watch(name string, watched *run, command *exec.Cmd, pipeReader *os.File, markDead, markExited context.CancelFunc) {
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		// The pipe must drain even when the log file fails: a full pipe would block the process.
		_, _ = io.Copy(&lossyWriter{target: watched.output, onLoss: func(err error) {
			supervisor.logger.Error("output of the process is lost", "process", name, "error", err)
		}}, pipeReader)
	}()

	_ = command.Wait()
	markDead()
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
	supervisor.saveRecord()
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

	supervisor.logger.Info("process stops", "process", name, "pid", stopped.pid)
	if spec.Stop == "" || !supervisor.runStopCommand(name, spec) {
		_ = syscall.Kill(-stopped.pid, syscall.SIGTERM)
	}
	// This time starts after the stop command, which can use all of its own time and still fail.
	grace := time.NewTimer(supervisor.stopTimeout)
	defer grace.Stop()
	select {
	case <-stopped.exited.Done():
		return
	case <-grace.C:
	}
	supervisor.logger.Warn("process killed, because it did not stop in time", "process", name, "pid", stopped.pid, "timeout", supervisor.stopTimeout.String())
	_ = syscall.Kill(-stopped.pid, syscall.SIGKILL)
	<-stopped.exited.Done()
}

func (supervisor *Supervisor) runStopCommand(name string, spec Spec) (hasSucceeded bool) {
	deadline, cancel := context.WithTimeout(context.Background(), supervisor.stopTimeout)
	defer cancel()
	command := shellCommand(deadline, spec, spec.Stop)
	// Not the writer of the process: that one closes when the process exits, and a stop command often lives longer.
	output, err := openRotatingFile(spec.LogFile, supervisor.logMaxBytes)
	if err != nil {
		supervisor.logger.Warn("the output of the stop command is lost", "process", name, "error", err)
	} else {
		defer output.Close()
		command.Stdout, command.Stderr = output, output
	}
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
	environment := maps.Clone(spec.Session)
	if environment == nil {
		environment = map[string]string{}
	}
	maps.Copy(environment, spec.Env)
	command.Env = make([]string, 0, len(environment))
	for key, value := range environment {
		command.Env = append(command.Env, key+"="+value)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second
	return command
}
