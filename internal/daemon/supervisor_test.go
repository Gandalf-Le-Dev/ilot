package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testWait = 10 * time.Second

func newTestSupervisor(t *testing.T, options SupervisorOptions) *Supervisor {
	t.Helper()
	supervisor := NewSupervisor(options)
	t.Cleanup(supervisor.Shutdown)
	return supervisor
}

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func isProcessAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func processNamed(t *testing.T, supervisor *Supervisor, name string) Process {
	t.Helper()
	for _, process := range supervisor.List() {
		if process.Name == name {
			return process
		}
	}
	t.Fatalf("the supervisor has no process %q", name)
	return Process{}
}

func waitForState(t *testing.T, supervisor *Supervisor, name string, state State) Process {
	t.Helper()
	waitUntil(t, fmt.Sprintf("%s is %s", name, state), func() bool {
		return processNamed(t, supervisor, name).State == state
	})
	return processNamed(t, supervisor, name)
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	var pid int
	waitUntil(t, "the process ID file "+path, func() bool {
		content, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(content)))
		return err == nil
	})
	return pid
}

func TestOutputAndExitCodeOfAProcess(t *testing.T) {
	supervisor := newTestSupervisor(t, SupervisorOptions{})
	logFile := filepath.Join(t.TempDir(), "logs", "api.log")

	started, err := supervisor.Put("api", Spec{Command: "echo to-stdout; echo to-stderr >&2; exit 3", LogFile: logFile})
	if err != nil {
		t.Fatal(err)
	}
	if started.PID == 0 || started.State != StateStarting {
		t.Fatalf("a started process has a process ID and is not ready yet, got %+v", started)
	}

	exited := waitForState(t, supervisor, "api", StateExited)
	if exited.ExitCode != 3 || exited.PID != 0 {
		t.Fatalf("got %+v, want exit code 3 and no process ID", exited)
	}
	output, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "to-stdout\nto-stderr\n" {
		t.Fatalf("log file holds %q", output)
	}
}

func TestProcessGetsOnlyTheEnvironmentOfItsSpecification(t *testing.T) {
	t.Setenv("ILOT_DAEMON_SECRET", "leak")
	supervisor := newTestSupervisor(t, SupervisorOptions{})
	workingDir := t.TempDir()
	logFile := filepath.Join(workingDir, "env.log")

	_, err := supervisor.Put("env", Spec{
		Command: `echo "secret=[$ILOT_DAEMON_SECRET] color=[$COLOR] dir=[$(basename "$(pwd)")]"`,
		Dir:     workingDir,
		Env:     map[string]string{"COLOR": "blue", "PATH": "/usr/bin:/bin"},
		LogFile: logFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, supervisor, "env", StateExited)
	output, _ := os.ReadFile(logFile)
	want := fmt.Sprintf("secret=[] color=[blue] dir=[%s]\n", filepath.Base(workingDir))
	if string(output) != want {
		t.Fatalf("got %q, want %q", output, want)
	}
}

func TestLogFileOfAProcessRotates(t *testing.T) {
	supervisor := newTestSupervisor(t, SupervisorOptions{LogMaxBytes: 1000})
	logFile := filepath.Join(t.TempDir(), "noisy.log")
	line := strings.Repeat("x", 99)

	_, err := supervisor.Put("noisy", Spec{
		Command: fmt.Sprintf("i=0; while [ $i -lt 40 ]; do echo %s; i=$((i+1)); sleep 0.02; done", line),
		Env:     map[string]string{"PATH": "/usr/bin:/bin"},
		LogFile: logFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, supervisor, "noisy", StateExited)

	for _, kept := range []string{logFile, logFile + ".1", logFile + ".2"} {
		info, err := os.Stat(kept)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() == 0 || info.Size() > 1000 {
			t.Errorf("%s holds %d bytes, want 1 to 1000", filepath.Base(kept), info.Size())
		}
	}
	if _, err := os.Stat(logFile + ".3"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a third old file exists: %v", err)
	}
}

func TestStopKillsAGrandchildThatIgnoresSIGTERM(t *testing.T) {
	supervisor := newTestSupervisor(t, SupervisorOptions{})
	workingDir := t.TempDir()
	pidFile := filepath.Join(workingDir, "grandchild.pid")

	started, err := supervisor.Put("tree", Spec{
		Command: `(trap '' TERM; sleep 300) & echo $! > grandchild.pid; wait`,
		Dir:     workingDir,
		Env:     map[string]string{"PATH": "/usr/bin:/bin"},
		LogFile: filepath.Join(workingDir, "tree.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	grandchild := readPIDFile(t, pidFile)
	if !isProcessAlive(grandchild) {
		t.Fatal("the grandchild does not run before the stop")
	}

	begin := time.Now()
	if err := supervisor.Delete("tree"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(begin); elapsed > defaultStopTimeout/2 {
		t.Errorf("the stop took %s, so it waited for the kill timeout", elapsed)
	}
	if len(supervisor.List()) != 0 {
		t.Errorf("a deleted process is still listed: %+v", supervisor.List())
	}
	waitUntil(t, "the shell and the grandchild are dead", func() bool {
		return !isProcessAlive(started.PID) && !isProcessAlive(grandchild)
	})
	if err := supervisor.Delete("tree"); err != nil {
		t.Errorf("a second delete fails: %v", err)
	}
}

func TestStopKillsAfterTheTimeout(t *testing.T) {
	supervisor := newTestSupervisor(t, SupervisorOptions{StopTimeout: 300 * time.Millisecond})
	workingDir := t.TempDir()

	started, err := supervisor.Put("stubborn", Spec{
		Command: `trap '' TERM; echo armed; sleep 300`,
		Env:     map[string]string{"PATH": "/usr/bin:/bin"},
		LogFile: filepath.Join(workingDir, "stubborn.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the trap is set", func() bool {
		output, _ := os.ReadFile(filepath.Join(workingDir, "stubborn.log"))
		return strings.Contains(string(output), "armed")
	})

	supervisor.Shutdown()
	stopped := processNamed(t, supervisor, "stubborn")
	if stopped.State != StateExited || stopped.ExitCode != 128+int(syscall.SIGKILL) {
		t.Fatalf("got %+v, want an exit by SIGKILL", stopped)
	}
	waitUntil(t, "the shell is dead", func() bool { return !isProcessAlive(started.PID) })
}

func TestStopRunsTheStopCommandInsteadOfSIGTERM(t *testing.T) {
	supervisor := newTestSupervisor(t, SupervisorOptions{})
	workingDir := t.TempDir()
	logFile := filepath.Join(workingDir, "polite.log")

	_, err := supervisor.Put("polite", Spec{
		Command: `while [ ! -f "$FLAG" ]; do sleep 0.05; done; exit 7`,
		Stop:    `echo stop-command-ran; touch "$FLAG"`,
		Dir:     workingDir,
		Env:     map[string]string{"PATH": "/usr/bin:/bin", "FLAG": "stop.flag"},
		LogFile: logFile,
	})
	if err != nil {
		t.Fatal(err)
	}

	supervisor.Shutdown()
	stopped := processNamed(t, supervisor, "polite")
	if stopped.ExitCode != 7 {
		t.Fatalf("got exit code %d, want 7: the process must end by itself after the stop command", stopped.ExitCode)
	}
	output, _ := os.ReadFile(logFile)
	if !strings.Contains(string(output), "stop-command-ran") {
		t.Errorf("the output of the stop command is not in the log file: %q", output)
	}
}

func TestAStopCommandWritesToTheLogAfterTheProcessExited(t *testing.T) {
	var daemonLog bytes.Buffer
	supervisor := newTestSupervisor(t, SupervisorOptions{Logger: slog.New(slog.NewTextHandler(&daemonLog, nil))})
	workingDir := t.TempDir()
	logFile := filepath.Join(workingDir, "app.log")

	_, err := supervisor.Put("app", Spec{
		Command: `while [ ! -f "$FLAG" ]; do sleep 0.05; done; exit 7`,
		Stop:    `touch "$FLAG"; sleep 0.5; echo late-output`,
		Dir:     workingDir,
		Env:     map[string]string{"PATH": "/usr/bin:/bin", "FLAG": "stop.flag"},
		LogFile: logFile,
	})
	if err != nil {
		t.Fatal(err)
	}

	supervisor.Shutdown()
	if stopped := processNamed(t, supervisor, "app"); stopped.ExitCode != 7 {
		t.Fatalf("got exit code %d, want 7", stopped.ExitCode)
	}
	output, _ := os.ReadFile(logFile)
	if !strings.Contains(string(output), "late-output") {
		t.Errorf("the late output of the stop command is not in the log file: %q", output)
	}
	if strings.Contains(daemonLog.String(), "the stop command failed") {
		t.Errorf("a stop command that worked is reported as failed:\n%s", daemonLog.String())
	}
}

func TestAFailedStopCommandFallsBackToSIGTERM(t *testing.T) {
	supervisor := newTestSupervisor(t, SupervisorOptions{})
	workingDir := t.TempDir()

	_, err := supervisor.Put("app", Spec{
		Command: "sleep 300",
		Stop:    "exit 1",
		Env:     map[string]string{"PATH": "/usr/bin:/bin"},
		LogFile: filepath.Join(workingDir, "app.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	supervisor.Shutdown()
	if stopped := processNamed(t, supervisor, "app"); stopped.ExitCode != 128+int(syscall.SIGTERM) {
		t.Fatalf("got exit code %d, want an exit by SIGTERM", stopped.ExitCode)
	}
}

func TestSIGTERMGetsItsOwnGraceAfterAStopCommandThatFails(t *testing.T) {
	supervisor := newTestSupervisor(t, SupervisorOptions{StopTimeout: time.Second})
	workingDir := t.TempDir()
	logFile := filepath.Join(workingDir, "app.log")

	_, err := supervisor.Put("app", Spec{
		Command: `trap 'sleep 0.5; echo graceful; exit 0' TERM; echo armed; sleep 300 & wait`,
		// It uses most of the stop timeout, so a shared deadline would leave the process no time to end.
		Stop:    "sleep 0.8; exit 1",
		Env:     map[string]string{"PATH": "/usr/bin:/bin"},
		LogFile: logFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the trap is set", func() bool {
		output, _ := os.ReadFile(logFile)
		return strings.Contains(string(output), "armed")
	})

	supervisor.Shutdown()
	if stopped := processNamed(t, supervisor, "app"); stopped.ExitCode != 0 {
		t.Fatalf("got exit code %d, want 0: the process must get the time to end by itself after SIGTERM", stopped.ExitCode)
	}
	output, _ := os.ReadFile(logFile)
	if !strings.Contains(string(output), "graceful") {
		t.Errorf("the process did not end by itself: %q", output)
	}
}

func TestPutKeepsALiveProcessWithTheSameSpecification(t *testing.T) {
	supervisor := newTestSupervisor(t, SupervisorOptions{})
	spec := Spec{Command: "sleep 300", Env: map[string]string{"PATH": "/usr/bin:/bin"}, LogFile: filepath.Join(t.TempDir(), "app.log")}

	first, err := supervisor.Put("app", spec)
	if err != nil {
		t.Fatal(err)
	}
	same, err := supervisor.Put("app", spec)
	if err != nil {
		t.Fatal(err)
	}
	if same.PID != first.PID {
		t.Fatalf("the same specification restarted the process: %d then %d", first.PID, same.PID)
	}

	changedSpec := spec
	changedSpec.Command = "sleep 301"
	changed, err := supervisor.Put("app", changedSpec)
	if err != nil {
		t.Fatal(err)
	}
	if changed.PID == first.PID || changed.Hash == first.Hash {
		t.Fatalf("a new specification did not replace the process: %+v then %+v", first, changed)
	}
	waitUntil(t, "the replaced process is dead", func() bool { return !isProcessAlive(first.PID) })
}

func TestPutStartsAnExitedProcessAgain(t *testing.T) {
	supervisor := newTestSupervisor(t, SupervisorOptions{})
	logFile := filepath.Join(t.TempDir(), "once.log")
	spec := Spec{Command: "echo run", LogFile: logFile}

	for range 2 {
		if _, err := supervisor.Put("once", spec); err != nil {
			t.Fatal(err)
		}
		waitForState(t, supervisor, "once", StateExited)
	}
	output, _ := os.ReadFile(logFile)
	if string(output) != "run\nrun\n" {
		t.Fatalf("log file holds %q, want two runs appended", output)
	}
}

func TestRestartStartsTheStoredSpecificationAgain(t *testing.T) {
	supervisor := newTestSupervisor(t, SupervisorOptions{})
	spec := Spec{Command: "sleep 300", Env: map[string]string{"PATH": "/usr/bin:/bin"}, LogFile: filepath.Join(t.TempDir(), "app.log")}

	first, err := supervisor.Put("app", spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := supervisor.Restart("app")
	if err != nil {
		t.Fatal(err)
	}
	if second.PID == first.PID || second.Hash != first.Hash || second.State != StateStarting {
		t.Fatalf("got %+v after a restart of %+v", second, first)
	}
	waitUntil(t, "the first process is dead", func() bool { return !isProcessAlive(first.PID) })

	if _, err := supervisor.Restart("unknown"); !errors.Is(err, ErrUnknownProcess) {
		t.Fatalf("a restart of an unknown process gives %v", err)
	}
}

func TestAStartThatFailsIsReported(t *testing.T) {
	supervisor := newTestSupervisor(t, SupervisorOptions{})
	workingDir := t.TempDir()

	_, err := supervisor.Put("app", Spec{Command: "true", Dir: filepath.Join(workingDir, "missing"), LogFile: filepath.Join(workingDir, "app.log")})
	if err == nil {
		t.Fatal("a start in a missing folder gives no error")
	}
	if failed := processNamed(t, supervisor, "app"); failed.State != StateExited || failed.ExitCode != exitCodeNotStarted {
		t.Fatalf("got %+v, want an exited process with the code of a failed start", failed)
	}
	if _, err := supervisor.Put("bad", Spec{LogFile: "x"}); err == nil {
		t.Fatal("a specification with no command is accepted")
	}
}

func TestShutdownRefusesLaterStarts(t *testing.T) {
	supervisor := newTestSupervisor(t, SupervisorOptions{})
	supervisor.Shutdown()
	_, err := supervisor.Put("app", Spec{Command: "sleep 300", LogFile: filepath.Join(t.TempDir(), "app.log")})
	if !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("got %v, want ErrShuttingDown", err)
	}
}

func TestHashOfASpecification(t *testing.T) {
	spec := Spec{Command: "run", Env: map[string]string{"A": "1", "B": "2"}, LogFile: "app.log"}
	same := Spec{Command: "run", Env: map[string]string{"B": "2", "A": "1"}, LogFile: "app.log"}
	if spec.Hash() != same.Hash() {
		t.Fatal("the order of the environment changes the hash")
	}
	other := same
	other.Env = map[string]string{"A": "1", "B": "3"}
	if spec.Hash() == other.Hash() {
		t.Fatal("a changed variable keeps the hash")
	}
}
