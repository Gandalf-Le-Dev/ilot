package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestEnsureWithACancelledContextStartsNothingAndRemovesNothing(t *testing.T) {
	paths := Paths{StateDir: shortDir(t)}
	leaveStaleSocket(t, paths.Socket())
	spawnCount := 0
	starter := Starter{Paths: paths, Spawn: func() error { spawnCount++; return nil }}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := starter.Ensure(cancelled)

	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrDaemonDown) {
		t.Errorf("got %v, want only the cancel", err)
	}
	if spawnCount != 0 {
		t.Errorf("a cancelled Ensure started %d daemons", spawnCount)
	}
	if _, err := os.Stat(paths.Socket()); err != nil {
		t.Errorf("a cancelled Ensure removed the socket file: %v", err)
	}
	if _, err := os.Stat(paths.LockFile()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a cancelled Ensure made a lock file: %v", err)
	}
}

func TestACancelDuringTheStartLeavesTheLiveSocket(t *testing.T) {
	helpers := newHelperDaemons(t)
	interrupted, cancel := context.WithCancel(t.Context())
	starter := helpers.starter()
	starter.Spawn = func() error {
		err := helpers.spawn()
		cancel()
		return err
	}

	if _, err := starter.Ensure(interrupted); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want the cancel", err)
	}

	client, err := helpers.starter().Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "one daemon lives", func() bool { return len(helpers.alive()) == 1 })
	if alive := helpers.alive(); alive[0] != status.PID {
		t.Fatalf("the live daemon is %d, but the socket answers for %d", alive[0], status.PID)
	}
}

func TestConcurrentStartsGiveOneDaemon(t *testing.T) {
	helpers := newHelperDaemons(t)
	const callers = 6

	daemonPIDs := make([]int, callers)
	failures := make([]error, callers)
	var group sync.WaitGroup
	for index := range callers {
		group.Go(func() {
			client, err := helpers.starter().Ensure(t.Context())
			if err != nil {
				failures[index] = err
				return
			}
			status, err := client.Status(t.Context())
			daemonPIDs[index], failures[index] = status.PID, err
		})
	}
	group.Wait()

	for index := range callers {
		if failures[index] != nil {
			t.Fatalf("caller %d: %v", index, failures[index])
		}
		if daemonPIDs[index] != daemonPIDs[0] {
			t.Fatalf("the callers reached several daemons: %v", daemonPIDs)
		}
	}
	if len(helpers.spawned()) < 2 {
		t.Logf("only %d daemon process started, so the lock saw no race in this run", len(helpers.spawned()))
	}
	waitUntil(t, "every daemon that lost the lock exited", func() bool { return len(helpers.alive()) == 1 })
	if alive := helpers.alive(); alive[0] != daemonPIDs[0] {
		t.Fatalf("the live daemon is %d, but the socket answers for %d", alive[0], daemonPIDs[0])
	}
	if _, err := NewClient(helpers.paths.Socket()).Status(t.Context()); err != nil {
		t.Fatalf("a daemon that lost the lock broke the socket of the winner: %v", err)
	}
}

func TestEnsureReplacesAStaleSocket(t *testing.T) {
	helpers := newHelperDaemons(t)
	leaveStaleSocket(t, helpers.paths.Socket())

	client, err := helpers.starter().Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.Version != "v-test" || status.PID != helpers.spawned()[0] {
		t.Fatalf("status is %+v, want the daemon %v", status, helpers.spawned())
	}
	lockContent, _ := os.ReadFile(helpers.paths.LockFile())
	if strings.TrimSpace(string(lockContent)) == "" {
		t.Error("the lock file does not hold the process ID of the daemon")
	}

	if err := client.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitUntilLockIsFree(t, helpers.paths)
	if _, err := os.Stat(helpers.paths.Socket()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a daemon that shut down left its socket file: %v", err)
	}
}

func TestEnsureStartsADaemonAgainWhenTheOldOneStillHeldTheLock(t *testing.T) {
	helpers := newHelperDaemons(t)
	oldDaemon, err := acquireLock(helpers.paths.LockFile())
	if err != nil {
		t.Fatal(err)
	}
	release := time.AfterFunc(300*time.Millisecond, func() { oldDaemon.Close() })
	defer release.Stop()

	client, err := helpers.starter().Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Status(t.Context()); err != nil {
		t.Fatal(err)
	}
	if spawned := helpers.spawned(); len(spawned) < 2 {
		t.Fatalf("the daemon answered with %d start, so the lock was not held", len(spawned))
	}
}

func TestEnsureGivesUpWhenNoDaemonAnswers(t *testing.T) {
	paths := Paths{StateDir: shortDir(t)}
	starter := Starter{Paths: paths, Spawn: func() error { return nil }, Timeout: 200 * time.Millisecond}

	_, err := starter.Ensure(t.Context())
	if !errors.Is(err, ErrDaemonDown) || !strings.Contains(err.Error(), paths.LogFile()) {
		t.Fatalf("got %v, want ErrDaemonDown with the path of the daemon log", err)
	}
}

func TestASecondRunLeavesTheFirstDaemonAlone(t *testing.T) {
	options := Options{Paths: Paths{StateDir: shortDir(t)}, Version: "v-first"}
	runContext, stopFirst := context.WithCancel(t.Context())
	firstDone := make(chan error, 1)
	go func() { firstDone <- Run(runContext, options) }()
	client := NewClient(options.Paths.Socket())
	waitUntil(t, "the first daemon answers", func() bool {
		_, err := client.Status(t.Context())
		return err == nil
	})

	if err := Run(t.Context(), options); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("a second daemon gives %v, want ErrAlreadyRunning", err)
	}
	if _, err := client.Status(t.Context()); err != nil {
		t.Fatalf("the second daemon broke the socket of the first: %v", err)
	}

	started, err := client.Put(t.Context(), "app", Spec{
		Command: "sleep 300",
		Env:     map[string]string{"PATH": "/usr/bin:/bin"},
		LogFile: filepath.Join(options.Paths.StateDir, "logs", "app.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	stopFirst()
	if err := <-firstDone; err != nil {
		t.Fatalf("a cancelled daemon returns %v", err)
	}
	waitUntil(t, "the daemon stopped its process", func() bool { return !isProcessAlive(started.PID) })
	if _, err := os.Stat(options.Paths.Socket()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the daemon left its socket file: %v", err)
	}
	daemonLog, _ := os.ReadFile(options.Paths.LogFile())
	if !strings.Contains(string(daemonLog), `"msg":"daemon started"`) || !strings.Contains(string(daemonLog), `"msg":"process started"`) {
		t.Errorf("daemon.log does not hold the JSON records:\n%s", daemonLog)
	}
}

func TestSocketPathFallsBackWhenTheStateFolderIsLong(t *testing.T) {
	short := Paths{StateDir: "/home/me/.local/state/ilot"}
	if got := short.Socket(); got != "/home/me/.local/state/ilot/daemon.sock" {
		t.Errorf("got %s", got)
	}
	long := Paths{StateDir: "/home/" + strings.Repeat("deep/", 20) + "ilot"}
	fallback := long.Socket()
	if len(fallback) > longestSocketPath || !strings.HasPrefix(fallback, "/tmp/ilot-") || !strings.HasSuffix(fallback, ".sock") {
		t.Errorf("got %s", fallback)
	}
	other := Paths{StateDir: long.StateDir + "2"}
	if other.Socket() == fallback {
		t.Error("two state folders share one socket path")
	}
}

func TestEnsureAfterAShutdownGivesANewDaemonEachTime(t *testing.T) {
	helpers := newHelperDaemons(t)
	lastPID := 0
	for iteration := range 30 {
		client, err := helpers.starter().Ensure(t.Context())
		if err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		status, err := client.Status(t.Context())
		if err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		if status.PID == lastPID {
			t.Fatalf("iteration %d: the daemon %d answers after its shutdown", iteration, lastPID)
		}
		lastPID = status.PID
		if err := client.Shutdown(t.Context()); err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		if _, err := os.Stat(helpers.paths.Socket()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("iteration %d: Shutdown returned while the socket file is still there: %v", iteration, err)
		}
	}
}

func TestEnsureWaitsForADaemonThatShutsDown(t *testing.T) {
	// Before the daemons, so that the folder is removed after the last daemon stopped the process that writes in it.
	workingDir := t.TempDir()
	helpers := newHelperDaemons(t)
	client, err := helpers.starter().Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	dying, err := client.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	slowToStop := Spec{
		Command: "echo $$ > app.pid; exec sleep 300",
		Stop:    "sleep 1; kill $(cat app.pid)",
		Dir:     workingDir,
		Env:     map[string]string{"PATH": "/usr/bin:/bin"},
		LogFile: filepath.Join(workingDir, "app.log"),
	}
	if _, err := client.Put(t.Context(), "app", slowToStop); err != nil {
		t.Fatal(err)
	}
	readPIDFile(t, filepath.Join(workingDir, "app.pid"))

	if err := syscall.Kill(dying.PID, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the daemon stops its process", func() bool {
		processes, err := client.Processes(t.Context())
		return err == nil && len(processes) == 1 && processes[0].State == StateStopping
	})

	client, err = helpers.starter().Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(t.Context(), "app", slowToStop); err != nil {
		t.Fatalf("Ensure gave a daemon that refuses a start: %v", err)
	}
	status, err := client.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.PID == dying.PID {
		t.Fatal("Ensure gave the daemon that shuts down")
	}
}

func TestCheckPrivateFolder(t *testing.T) {
	parent := t.TempDir()
	private := filepath.Join(parent, "private")
	shared := filepath.Join(parent, "shared")
	link := filepath.Join(parent, "link")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	// Not in Mkdir, where the umask would remove the bits.
	if err := os.Chmod(shared, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}

	if err := checkPrivateFolder(private); err != nil {
		t.Errorf("a folder with the mode 0700 is refused: %v", err)
	}
	if err := checkPrivateFolder(filepath.Join(parent, "missing")); err != nil {
		t.Errorf("a folder that does not exist is refused: %v", err)
	}
	if err := checkPrivateFolder(shared); !errors.Is(err, ErrUnsafeSocketFolder) {
		t.Errorf("a folder that the group can write gives %v", err)
	}
	if err := checkPrivateFolder(link); !errors.Is(err, ErrUnsafeSocketFolder) {
		t.Errorf("a link to a folder gives %v", err)
	}
	if err := checkSocketFolder(filepath.Join(shared, "daemon.sock")); err != nil {
		t.Errorf("a state folder is held to the rule of the fallback folder: %v", err)
	}
}
