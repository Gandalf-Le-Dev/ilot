package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLaunchCreatesTheLogOfTheDaemonForTheUserOnly(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "daemon.log")

	if _, err := (Launch{Binary: "/usr/bin/true", LogFile: logFile}).Start(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != daemonLogPerm {
		t.Errorf("daemon.log has the mode %v, want %v", info.Mode().Perm(), os.FileMode(daemonLogPerm))
	}
}

func TestLaunchNarrowsTheModeOfALogThatAnOlderVersionCreated(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(logFile, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := (Launch{Binary: "/usr/bin/true", LogFile: logFile}).Start(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != daemonLogPerm {
		t.Errorf("daemon.log has the mode %v, want %v", info.Mode().Perm(), os.FileMode(daemonLogPerm))
	}
}
