package main

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/ilotctl"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

func startInFolder(t *testing.T, dir string, supervisor *readySupervisor, appNames ...string) error {
	t.Helper()
	currentWorkspace, worktree, err := workspace.Find(dir, t.TempDir(), testGlobal, workspace.NewGit())
	if err != nil {
		t.Fatal(err)
	}
	currentWorkspace, store, err := withBindings(currentWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	currentWorkspace, stackName, err := stackOfWorktree(t.Context(), store, currentWorkspace, worktree, true, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := newController(t.Context(), currentWorkspace, store, stackName, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	controller.Supervisor = supervisor
	controller.ShellEnv = func() (map[string]string, error) { return map[string]string{}, nil }
	controller.Output = io.Discard
	return controller.Start(t.Context(), stackName, appNames, ilotctl.StartOptions{SkipDependencies: false})
}

// The daemon cannot start a process in a folder that is gone: "fork/exec /bin/sh: no such file or directory".
func TestAStackWhoseBoundWorktreeIsDeletedRunsFromTheMainCheckout(t *testing.T) {
	shopDir, worktreeDir := shopWithAWorktree(t)
	supervisor := &readySupervisor{started: map[string]bool{}, dirs: map[string]string{}}
	if err := startInFolder(t, worktreeDir, supervisor, "api"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(worktreeDir); err != nil {
		t.Fatal(err)
	}

	if err := startInFolder(t, shopDir, supervisor, "api"); err != nil {
		t.Fatalf("a start in base after the worktree of retry-jobs was deleted = %v", err)
	}

	for processName, dir := range supervisor.dirs {
		if dir == worktreeDir {
			t.Errorf("%s got the deleted folder %s", processName, dir)
		}
	}
}
