package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

func cleanInFolder(t *testing.T, dir string, isDryRun bool) (string, workspace.Workspace) {
	t.Helper()
	currentWorkspace, _, err := workspace.Find(dir, t.TempDir(), testGlobal, workspace.NewGit())
	if err != nil {
		t.Fatal(err)
	}
	currentWorkspace, store, err := withBindings(currentWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := newExecutor(t.Context(), currentWorkspace, store, workspace.BaseStackName, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	executor.Supervisor = &readySupervisor{started: map[string]bool{}, dirs: nil}
	var output bytes.Buffer
	if err := cleanStacks(t.Context(), &output, currentWorkspace, store, executor, isDryRun); err != nil {
		t.Fatal(err)
	}
	afterClean, _, err := workspace.Find(dir, t.TempDir(), testGlobal, workspace.NewGit())
	if err != nil {
		t.Fatal(err)
	}
	afterClean, _, err = withBindings(afterClean)
	if err != nil {
		t.Fatal(err)
	}
	return output.String(), afterClean
}

func TestCleanKeepsAStackWhoseWorktreeWasMoved(t *testing.T) {
	shopDir, worktreeDir := shopWithAWorktree(t)
	if err := startInFolder(t, worktreeDir, &readySupervisor{started: map[string]bool{}, dirs: nil}, "api"); err != nil {
		t.Fatal(err)
	}
	movedDir := filepath.Join(filepath.Dir(shopDir), "shop-retry-moved")
	runGitInTest(t, shopDir, "worktree", "move", worktreeDir, movedDir)

	output, afterClean := cleanInFolder(t, shopDir, false)

	if !strings.Contains(output, "kept retry-jobs") || strings.Contains(output, "removed") {
		t.Errorf("clean said %q, want the stack kept", output)
	}
	if got := afterClean.Bindings["retry-jobs"]["shop"]; got != movedDir {
		t.Errorf("retry-jobs is bound to %q, want the moved worktree %s", got, movedDir)
	}
}

func TestCleanNamesTheAppsThatItStops(t *testing.T) {
	shopDir, worktreeDir := shopWithAWorktree(t)
	if err := startInFolder(t, worktreeDir, &readySupervisor{started: map[string]bool{}, dirs: nil}, "api"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(worktreeDir); err != nil {
		t.Fatal(err)
	}

	output, afterClean := cleanInFolder(t, shopDir, false)

	if want := "removed retry-jobs\n  stopped api\n"; output != want {
		t.Errorf("clean said %q, want %q", output, want)
	}
	if len(afterClean.Bindings) != 0 {
		t.Errorf("bindings after clean = %v, want none", afterClean.Bindings)
	}
}

func TestADryRunOfCleanSaysWhatItWouldDoAndDeletesNothing(t *testing.T) {
	shopDir, worktreeDir := shopWithAWorktree(t)
	if err := startInFolder(t, worktreeDir, &readySupervisor{started: map[string]bool{}, dirs: nil}, "api"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(worktreeDir); err != nil {
		t.Fatal(err)
	}

	output, afterClean := cleanInFolder(t, shopDir, true)

	if want := "would remove retry-jobs\n  would stop api\n"; output != want {
		t.Errorf("clean --dry-run said %q, want %q", output, want)
	}
	if _, isKept := afterClean.Bindings["retry-jobs"]; !isKept {
		t.Errorf("bindings after clean --dry-run = %v, want retry-jobs kept", afterClean.Bindings)
	}
}

func TestADryRunOfCleanDoesNotBindAMovedWorktree(t *testing.T) {
	shopDir, worktreeDir := shopWithAWorktree(t)
	if err := startInFolder(t, worktreeDir, &readySupervisor{started: map[string]bool{}, dirs: nil}, "api"); err != nil {
		t.Fatal(err)
	}
	movedDir := filepath.Join(filepath.Dir(shopDir), "shop-retry-moved")
	runGitInTest(t, shopDir, "worktree", "move", worktreeDir, movedDir)

	output, afterClean := cleanInFolder(t, shopDir, true)

	if !strings.Contains(output, "would keep retry-jobs") {
		t.Errorf("clean --dry-run said %q, want the stack that it would keep", output)
	}
	if got := afterClean.Bindings["retry-jobs"]["shop"]; got != worktreeDir {
		t.Errorf("retry-jobs is bound to %q, want the old folder %s: a dry run changes nothing", got, worktreeDir)
	}
}
