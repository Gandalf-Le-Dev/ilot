package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func startTree(t *testing.T, client *Client, name, workingDir string) (shell, grandchild int) {
	t.Helper()
	started, err := client.Put(t.Context(), name, Spec{
		Command: "sleep 300 & echo $! > " + name + ".pid; wait",
		Dir:     workingDir,
		Env:     map[string]string{"PATH": "/usr/bin:/bin"},
		LogFile: filepath.Join(workingDir, name+".log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return started.PID, readPIDFile(t, filepath.Join(workingDir, name+".pid"))
}

func TestANewDaemonKillsTheProcessesOfAKilledDaemon(t *testing.T) {
	helpers := newHelperDaemons(t)
	workingDir := t.TempDir()
	client, err := helpers.starter().Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wholeShell, wholeGrandchild := startTree(t, client, "whole", workingDir)
	headlessShell, headlessGrandchild := startTree(t, client, "headless", workingDir)
	orphans := []int{wholeShell, wholeGrandchild, headlessGrandchild}
	// Safety net: if the product fails, the test still leaves no process.
	t.Cleanup(func() {
		for _, pid := range append(orphans, headlessShell) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	if err := syscall.Kill(status.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitUntilLockIsFree(t, helpers.paths)
	// A group whose shell is dead must be found too.
	if err := syscall.Kill(headlessShell, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the shell of the second group is dead", func() bool { return !isProcessAlive(headlessShell) })
	for _, pid := range orphans {
		if !isProcessAlive(pid) {
			t.Fatalf("process %d died with the daemon, so the test proves nothing", pid)
		}
	}

	client, err = helpers.starter().Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	newStatus, err := client.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if newStatus.PID == status.PID {
		t.Fatal("the killed daemon still answers")
	}
	// The daemon listens only after the cleanup, so no wait is needed here.
	for _, pid := range orphans {
		if isProcessAlive(pid) {
			t.Errorf("process %d of the killed daemon still runs", pid)
		}
	}
	processes, err := client.Processes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(processes) != 0 {
		t.Errorf("the new daemon lists %+v", processes)
	}
}

func startGroup(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	command := exec.Command("/bin/sh", "-c", script)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_ = command.Wait()
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-exited
	})
	return command
}

func recordOf(t *testing.T, records map[string]processRecord) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "processes.json")
	if err := writeRecords(path, records); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOrphanCleanupSparesAProcessThatReusesARecordedID(t *testing.T) {
	stranger := startGroup(t, "sleep 300")
	pid := stranger.Process.Pid
	if _, isAlive := processStartTime(pid); !isAlive {
		t.Fatal("the start time of a live process is not readable")
	}
	discard := slog.New(slog.DiscardHandler)

	for _, startedAt := range []string{"", "another start time"} {
		recordPath := recordOf(t, map[string]processRecord{"app": {PID: pid, StartedAt: startedAt}})
		killOrphans(recordPath, time.Second, discard)
		time.Sleep(50 * time.Millisecond)
		if !groupExists(pid) {
			t.Fatalf("the cleanup killed a process with the recorded ID and the start time %q", startedAt)
		}
		if _, err := os.Stat(recordPath); err == nil {
			t.Error("the cleanup kept the record file")
		}
	}

	killOrphans(recordOf(t, map[string]processRecord{"low": {PID: 1}, "none": {PID: 0}, "negative": {PID: -1}}), time.Second, discard)
	if !groupExists(pid) {
		t.Fatal("a record with a special process ID reached other processes")
	}
}

func TestOrphanCleanupKillsAGroupThatIgnoresSIGTERM(t *testing.T) {
	workingDir := t.TempDir()
	stubborn := startGroup(t, "trap '' TERM; echo armed > "+filepath.Join(workingDir, "armed")+"; sleep 300")
	pid := stubborn.Process.Pid
	waitUntil(t, "the trap is set", func() bool {
		_, err := os.Stat(filepath.Join(workingDir, "armed"))
		return err == nil
	})
	startedAt, _ := processStartTime(pid)

	killOrphans(recordOf(t, map[string]processRecord{"app": {PID: pid, StartedAt: startedAt}}), 200*time.Millisecond, slog.New(slog.DiscardHandler))

	waitUntil(t, "the group is gone", func() bool { return !groupExists(pid) })
}

func TestSupervisorRecordsOnlyLiveProcesses(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "processes.json")
	supervisor := newTestSupervisor(t, SupervisorOptions{RecordFile: recordPath})
	workingDir := t.TempDir()
	spec := Spec{Command: "sleep 300", Env: map[string]string{"PATH": "/usr/bin:/bin"}, LogFile: filepath.Join(workingDir, "app.log")}

	started, err := supervisor.Put("app", spec)
	if err != nil {
		t.Fatal(err)
	}
	wantStartedAt, _ := processStartTime(started.PID)
	records := readRecords(t, recordPath)
	if record := records["app"]; record.PID != started.PID || record.StartedAt == "" || record.StartedAt != wantStartedAt {
		t.Fatalf("the record holds %+v for the process %d that started at %q", records, started.PID, wantStartedAt)
	}

	if err := supervisor.Delete("app"); err != nil {
		t.Fatal(err)
	}
	if records := readRecords(t, recordPath); len(records) != 0 {
		t.Fatalf("after the stop the record holds %+v", records)
	}
}

func readRecords(t *testing.T, path string) map[string]processRecord {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]processRecord{}
	if err := json.Unmarshal(content, &records); err != nil {
		t.Fatal(err)
	}
	return records
}
