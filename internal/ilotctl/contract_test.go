package ilotctl

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
)

// supervisorUnderContract gives the contract test one supervisor and three processes:
// one that stays alive, one that ends at every start, and one whose probe never passes.
type supervisorUnderContract struct {
	supervisor ProcessSupervisor
	steady     ProcessSpec
	crashing   ProcessSpec
	neverReady ProcessSpec
	startsOf   func(t *testing.T, processName string) int
}

func TestTheFakeSupervisorKeepsTheContract(t *testing.T) {
	fake := &fakeSupervisor{runningSpecs: map[string]ProcessSpec{}}
	fake.setBehavior("crashing", processEndsAtEveryStart)
	fake.setBehavior("never-ready", processStaysNotReady)
	spec := func(name string) ProcessSpec {
		return ProcessSpec{Name: name, Spec: daemon.Spec{Command: name, LogFile: "/logs/" + name + ".log"}}
	}
	checkSupervisorContract(t, supervisorUnderContract{
		supervisor: fake,
		steady:     spec("steady"),
		crashing:   spec("crashing"),
		neverReady: spec("never-ready"),
		startsOf: func(_ *testing.T, processName string) int {
			starts := 0
			for _, event := range fake.events {
				if event == "start "+processName || event == "restart "+processName {
					starts++
				}
			}
			return starts
		},
	})
}

func TestTheDaemonSupervisorKeepsTheContract(t *testing.T) {
	// A unix socket path holds about 100 bytes, and the temporary folder of a test is longer on macOS.
	stateDir, err := os.MkdirTemp("/tmp", "ilot")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(stateDir) })
	paths := daemon.Paths{StateDir: stateDir}
	daemonContext, stopDaemon := context.WithCancel(context.Background())
	daemonEnded := make(chan error, 8)
	spawned := 0
	supervisor := &DaemonSupervisor{
		Starter: daemon.Starter{Paths: paths, Spawn: func() error {
			spawned++
			go func() {
				daemonEnded <- daemon.Run(daemonContext, daemon.Options{
					Paths:      paths,
					Version:    "test",
					Supervisor: daemon.SupervisorOptions{ProbeInterval: 20 * time.Millisecond, AliveWindow: 50 * time.Millisecond, StopTimeout: 2 * time.Second},
				})
			}()
			return nil
		}},
		Version:  "test",
		Warnings: nil,
	}
	t.Cleanup(func() {
		stopDaemon()
		for range spawned {
			if err := <-daemonEnded; err != nil && !errors.Is(err, daemon.ErrAlreadyRunning) {
				t.Errorf("the daemon ended with %v", err)
			}
		}
	})

	workDir := t.TempDir()
	startsFile := func(processName string) string { return filepath.Join(workDir, processName+".starts") }
	spec := func(name, command string, ready *daemon.Probe) ProcessSpec {
		return ProcessSpec{Name: name, Spec: daemon.Spec{
			Command: "echo start >> " + plan.ShellQuote(startsFile(name)) + "; " + command,
			Dir:     workDir,
			Env:     map[string]string{"PATH": os.Getenv("PATH")},
			LogFile: filepath.Join(workDir, name+".log"),
			Ready:   ready,
		}}
	}
	checkSupervisorContract(t, supervisorUnderContract{
		supervisor: supervisor,
		steady:     spec("steady", "exec sleep 600", nil),
		crashing:   spec("crashing", "exit 1", nil),
		neverReady: spec("never-ready", "exec sleep 600", &daemon.Probe{Port: portWithNoListener(t)}),
		startsOf: func(t *testing.T, processName string) int {
			t.Helper()
			content, err := os.ReadFile(startsFile(processName))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			return strings.Count(string(content), "start\n")
		},
	})
}

func portWithNoListener(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func checkSupervisorContract(t *testing.T, subject supervisorUnderContract) {
	t.Helper()
	ctx := t.Context()
	supervisor := subject.supervisor
	const longWait = 10 * time.Second
	stateOf := func(processName string) (daemon.Process, bool) {
		t.Helper()
		processByName, err := supervisor.Processes(ctx)
		if err != nil {
			t.Fatalf("Processes: %v", err)
		}
		process, isKnown := processByName[processName]
		return process, isKnown
	}

	if processByName, err := supervisor.Processes(ctx); err != nil || len(processByName) != 0 {
		t.Fatalf("Processes with no daemon = %v, %v; want none and no error", processByName, err)
	}
	if supervisor.IsUp(ctx) {
		t.Fatal("IsUp = true after a read: a read must not start the daemon")
	}
	someRoutes := []daemon.Route{{Host: "steady.base.localhost", Port: 20000}}
	if err := supervisor.SetRoutes(ctx, someRoutes); err != nil || supervisor.IsUp(ctx) {
		t.Fatalf("SetRoutes with no daemon = %v, IsUp = %t; want no error and no daemon", err, supervisor.IsUp(ctx))
	}
	if err := supervisor.Apply(ctx, nil); err != nil || supervisor.IsUp(ctx) {
		t.Fatalf("Apply of nothing = %v, IsUp = %t; want no daemon for no process", err, supervisor.IsUp(ctx))
	}

	if err := supervisor.Apply(ctx, []ProcessSpec{subject.steady}); err != nil {
		t.Fatalf("Apply(steady): %v", err)
	}
	if err := supervisor.WaitReady(ctx, subject.steady.Name, longWait); err != nil {
		t.Fatalf("WaitReady(steady): %v", err)
	}
	steady, isKnown := stateOf(subject.steady.Name)
	if !isKnown || steady.State != daemon.StateReady || steady.Hash != subject.steady.Hash() || !supervisor.IsUp(ctx) {
		t.Fatalf("steady = %+v, known %t, IsUp %t; want a ready process with the hash of its specification", steady, isKnown, supervisor.IsUp(ctx))
	}

	if err := supervisor.SetRoutes(ctx, someRoutes); err != nil {
		t.Fatalf("SetRoutes: %v", err)
	}

	withCrashing := []ProcessSpec{subject.steady, subject.crashing}
	if err := supervisor.Apply(ctx, withCrashing); err != nil {
		t.Fatalf("Apply(steady, crashing): %v", err)
	}
	if _, hasEnded := errors.AsType[*daemon.ExitedError](supervisor.WaitReady(ctx, subject.crashing.Name, longWait)); !hasEnded {
		t.Fatal("WaitReady(crashing) gives no ExitedError")
	}
	if crashing, _ := stateOf(subject.crashing.Name); crashing.State != daemon.StateExited {
		t.Fatalf("crashing = %+v, want an exited process that the supervisor still knows", crashing)
	}
	if err := supervisor.Apply(ctx, withCrashing); err != nil {
		t.Fatalf("second Apply(steady, crashing): %v", err)
	}
	if starts := subject.startsOf(t, subject.crashing.Name); starts != 1 {
		t.Fatalf("crashing started %d times, want 1: Apply leaves an ended process as it is", starts)
	}
	if starts := subject.startsOf(t, subject.steady.Name); starts != 1 {
		t.Fatalf("steady started %d times, want 1: Apply leaves an unchanged process as it is", starts)
	}

	if err := supervisor.Restart(ctx, subject.crashing.Name); err != nil {
		t.Fatalf("Restart(crashing): %v", err)
	}
	if _, hasEnded := errors.AsType[*daemon.ExitedError](supervisor.WaitReady(ctx, subject.crashing.Name, longWait)); !hasEnded {
		t.Fatal("WaitReady(crashing) after a restart gives no ExitedError")
	}
	if starts := subject.startsOf(t, subject.crashing.Name); starts != 2 {
		t.Fatalf("crashing started %d times after a restart, want 2", starts)
	}
	if err := supervisor.Restart(ctx, "unknown"); !errors.Is(err, daemon.ErrUnknownProcess) {
		t.Fatalf("Restart(unknown) = %v, want ErrUnknownProcess", err)
	}

	if err := supervisor.Apply(ctx, []ProcessSpec{subject.steady, subject.neverReady}); err != nil {
		t.Fatalf("Apply(steady, neverReady): %v", err)
	}
	if _, isKnown := stateOf(subject.crashing.Name); isKnown {
		t.Fatal("crashing is still known after an Apply that does not name it")
	}
	waitErr := supervisor.WaitReady(ctx, subject.neverReady.Name, 300*time.Millisecond)
	if _, isNotReady := errors.AsType[*daemon.NotReadyError](waitErr); !isNotReady {
		t.Fatalf("WaitReady(neverReady) = %v, want a NotReadyError after the timeout", waitErr)
	}
	if neverReady, _ := stateOf(subject.neverReady.Name); neverReady.State != daemon.StateStarting {
		t.Fatalf("neverReady = %+v, want a process that still starts: a timeout stops nothing", neverReady)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := supervisor.WaitReady(cancelled, subject.neverReady.Name, longWait); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitReady with a cancelled context = %v, want context.Canceled", err)
	}

	if err := supervisor.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if processByName, err := supervisor.Processes(ctx); err != nil || len(processByName) != 0 || supervisor.IsUp(ctx) {
		t.Fatalf("after Shutdown: Processes = %v, %v, IsUp = %t; want nothing", processByName, err, supervisor.IsUp(ctx))
	}
	if err := supervisor.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown with no daemon: %v", err)
	}
}
