package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"testing"
	"time"
)

const helperStateDirVariable = "ILOT_TEST_DAEMON_STATE"

// TestMain turns the test binary into a daemon when a test starts it with the helper variable.
// The tests of the start, of the lock and of a killed daemon need a real second process.
func TestMain(m *testing.M) {
	stateDir := os.Getenv(helperStateDirVariable)
	if stateDir == "" {
		os.Exit(m.Run())
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	err := Run(ctx, Options{
		Paths:      Paths{StateDir: stateDir},
		Version:    "v-test",
		Supervisor: SupervisorOptions{ProbeInterval: 20 * time.Millisecond},
	})
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type helperDaemons struct {
	paths Paths

	mu   sync.Mutex
	pids []int
}

func newHelperDaemons(t *testing.T) *helperDaemons {
	t.Helper()
	helpers := &helperDaemons{paths: Paths{StateDir: shortDir(t)}}
	t.Cleanup(func() {
		stopContext, cancel := context.WithTimeout(context.Background(), testWait)
		defer cancel()
		_ = NewClient(helpers.paths.Socket()).Shutdown(stopContext)
		deadline := time.Now().Add(testWait)
		for len(helpers.alive()) > 0 {
			if time.Now().After(deadline) {
				for _, pid := range helpers.alive() {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
				t.Errorf("a daemon of the test did not exit and was killed")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	return helpers
}

func (helpers *helperDaemons) spawn() error {
	launch := Launch{
		Binary:  os.Args[0],
		Env:     append(os.Environ(), helperStateDirVariable+"="+helpers.paths.StateDir),
		LogFile: helpers.paths.LogFile(),
	}
	pid, err := launch.Start()
	if err != nil {
		return err
	}
	helpers.mu.Lock()
	helpers.pids = append(helpers.pids, pid)
	helpers.mu.Unlock()
	return nil
}

func (helpers *helperDaemons) spawned() []int {
	helpers.mu.Lock()
	defer helpers.mu.Unlock()
	return append([]int(nil), helpers.pids...)
}

func (helpers *helperDaemons) alive() []int {
	var alive []int
	for _, pid := range helpers.spawned() {
		if isProcessAlive(pid) {
			alive = append(alive, pid)
		}
	}
	return alive
}

func (helpers *helperDaemons) starter() Starter {
	return Starter{Paths: helpers.paths, Spawn: helpers.spawn}
}

func waitUntilLockIsFree(t *testing.T, paths Paths) {
	t.Helper()
	waitUntil(t, "the daemon lock is free", func() bool {
		lock, err := acquireLock(paths.LockFile())
		if errors.Is(err, ErrAlreadyRunning) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		lock.Close()
		return true
	})
}
