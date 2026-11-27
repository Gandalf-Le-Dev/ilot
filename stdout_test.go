package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
)

// captureOutput runs a command with its stdout and its stderr in files, so that a test reads each one apart.
func captureOutput(t *testing.T, run func() error) (stdout, stderr string, err error) {
	t.Helper()
	stdoutFile, stderrFile := filepath.Join(t.TempDir(), "stdout"), filepath.Join(t.TempDir(), "stderr")
	files := map[string]*os.File{}
	for _, path := range []string{stdoutFile, stderrFile} {
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		files[path] = file
	}
	savedStdout, savedStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = files[stdoutFile], files[stderrFile]
	err = run()
	os.Stdout, os.Stderr = savedStdout, savedStderr
	for path, file := range files {
		file.Close()
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if path == stdoutFile {
			stdout = string(content)
		} else {
			stderr = string(content)
		}
	}
	return stdout, stderr, err
}

// withADaemonOfAnotherVersion runs a daemon that every command that calls it warns about.
func withADaemonOfAnotherVersion(t *testing.T) {
	t.Helper()
	// Short, so that the socket fits in the state folder on macOS.
	stateHome, err := os.MkdirTemp("/tmp", "ilot-out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(stateHome) })
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("HOME", canonicalTempDir(t))
	t.Setenv("SHELL", "/bin/sh")
	paths := daemon.Paths{StateDir: filepath.Join(stateHome, "ilot")}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() {
		stopped <- daemon.Run(ctx, daemon.Options{Paths: paths, Version: "v0.0.1-old", ProxyListen: ""})
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	client := daemon.NewClient(paths.Socket())
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := client.Status(t.Context()); err == nil {
			return
		} else if !errors.Is(err, daemon.ErrDaemonDown) || time.Now().After(deadline) {
			t.Fatalf("the daemon of the test does not answer: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTheDataCommandsPrintOnlyDataOnStdoutWhenAWarningIsPresent(t *testing.T) {
	shopDir, worktreeDir := shopWithAWorktree(t)
	withADaemonOfAnotherVersion(t)
	if err := startInFolder(t, shopDir, &readySupervisor{started: map[string]bool{}, dirs: nil}, "api"); err != nil {
		t.Fatal(err)
	}

	t.Chdir(shopDir)
	stdout, stderr, err := captureOutput(t, func() error { return (&lsCommand{JSON: true}).Run(t.Context()) })
	if err != nil {
		t.Fatalf("ilot ls --json: %v", err)
	}
	if !strings.Contains(stderr, `warning: the ilot daemon has the version "v0.0.1-old"`) {
		t.Errorf("stderr of ilot ls --json = %q, want the warning about the daemon", stderr)
	}
	requireOneJSONObject(t, "ilot ls --json", stdout)

	t.Chdir(worktreeDir)
	stdout, _, err = captureOutput(t, func() error { return (&appsCommand{JSON: true}).Run(t.Context()) })
	if err != nil {
		t.Fatalf("ilot apps --json: %v", err)
	}
	requireOneJSONObject(t, "ilot apps --json", stdout)

	t.Chdir(shopDir)
	stdout, _, err = captureOutput(t, func() error { return (&envCommand{App: "api"}).Run(t.Context()) })
	if err != nil {
		t.Fatalf("ilot env api: %v", err)
	}
	for line := range strings.Lines(stdout) {
		if key, _, isPair := strings.Cut(line, "="); !isPair || strings.Contains(key, " ") || strings.HasPrefix(line, "warning") {
			t.Errorf("ilot env api printed %q, which is not a variable", line)
		}
		sourceStart := strings.LastIndex(line, "(")
		if sourceStart < 0 {
			t.Errorf("ilot env api printed %q, which names no source", line)
			continue
		}
		beforeSource := line[:sourceStart]
		source := strings.TrimSpace(line[len(beforeSource):])
		gap := len(beforeSource) - len(strings.TrimRight(beforeSource, " "))
		valueMayEndWithSpaces := source == "(shell)" || source == "(session)"
		if gap < 2 || (gap > 2 && !valueMayEndWithSpaces) {
			t.Errorf("ilot env api printed %q, want exactly two spaces between the value and the source", line)
		}
	}
}

func requireOneJSONObject(t *testing.T, command, stdout string) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader([]byte(stdout)))
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		t.Fatalf("%s printed %q, which is not one JSON object: %v", command, stdout, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		t.Errorf("%s printed more than one JSON object: %q", command, stdout)
	}
}
