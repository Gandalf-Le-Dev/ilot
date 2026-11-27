package main

import (
	"os"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

// fromAnotherFolder runs the rest of a test in a folder that is in no repo, with a home folder that has no global file.
func fromAnotherFolder(t *testing.T) {
	t.Helper()
	t.Chdir(canonicalTempDir(t))
	t.Setenv("HOME", canonicalTempDir(t))
}

func stateOfTheTest(t *testing.T) state.State {
	t.Helper()
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	current, err := state.Store{RootDir: stateDirOf(homeDir)}.Load()
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func TestRmWithTheWorkspaceFlagWorksFromAnyFolder(t *testing.T) {
	_, worktreeDir := shopWithAWorktree(t)
	if err := startInFolder(t, worktreeDir, &readySupervisor{started: map[string]bool{}, dirs: nil}, "api"); err != nil {
		t.Fatal(err)
	}
	fromAnotherFolder(t)

	if err := (&rmCommand{Stack: "retry-jobs", Selection: workspaceSelection{Workspace: "shop"}}).Run(t.Context()); err != nil {
		t.Fatalf("ilot rm --workspace shop retry-jobs: %v", err)
	}

	if _, isKept := stateOfTheTest(t).Stacks("shop")["retry-jobs"]; isKept {
		t.Error("the stack retry-jobs is still in the state")
	}
}

func TestCleanWithTheWorkspaceFlagWorksFromAnyFolder(t *testing.T) {
	_, worktreeDir := shopWithAWorktree(t)
	if err := startInFolder(t, worktreeDir, &readySupervisor{started: map[string]bool{}, dirs: nil}, "api"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(worktreeDir); err != nil {
		t.Fatal(err)
	}
	fromAnotherFolder(t)

	if err := (&cleanCommand{DryRun: false, Selection: workspaceSelection{Workspace: "shop"}}).Run(t.Context()); err != nil {
		t.Fatalf("ilot clean --workspace shop: %v", err)
	}

	if _, isKept := stateOfTheTest(t).Stacks("shop")["retry-jobs"]; isKept {
		t.Error("the stack retry-jobs is still in the state")
	}
}

func TestLsWithTheWorkspaceFlagWorksFromAnyFolder(t *testing.T) {
	_, worktreeDir := shopWithAWorktree(t)
	if err := startInFolder(t, worktreeDir, &readySupervisor{started: map[string]bool{}, dirs: nil}, "api"); err != nil {
		t.Fatal(err)
	}
	fromAnotherFolder(t)

	if err := (&lsCommand{JSON: true, Selection: workspaceSelection{Workspace: "shop"}}).Run(t.Context()); err != nil {
		t.Fatalf("ilot ls --workspace shop: %v", err)
	}
}

func TestTheStackFlagSelectsAStackFromTheMainCheckout(t *testing.T) {
	shopDir, worktreeDir := shopWithAWorktree(t)
	if err := startInFolder(t, worktreeDir, &readySupervisor{started: map[string]bool{}, dirs: nil}, "api"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(shopDir)

	if err := (&stopCommand{Apps: []string{"api"}, All: false, Selection: selection{Workspace: "", Stack: "retry-jobs"}}).Run(t.Context()); err != nil {
		t.Fatalf("ilot stop --stack retry-jobs api: %v", err)
	}

	if stateOfTheTest(t).App("shop", "retry-jobs", "api").Wanted {
		t.Error("api of retry-jobs is still wanted after ilot stop --stack retry-jobs api")
	}
}

func TestStopAllStopsEveryAppOfTheStack(t *testing.T) {
	_, worktreeDir := shopWithAWorktree(t)
	if err := startInFolder(t, worktreeDir, &readySupervisor{started: map[string]bool{}, dirs: nil}, "api"); err != nil {
		t.Fatal(err)
	}
	fromAnotherFolder(t)

	if err := (&stopCommand{Apps: nil, All: true, Selection: selection{Workspace: "shop", Stack: "retry-jobs"}}).Run(t.Context()); err != nil {
		t.Fatalf("ilot stop --all --workspace shop --stack retry-jobs: %v", err)
	}

	if stateOfTheTest(t).App("shop", "retry-jobs", "api").Wanted {
		t.Error("api of retry-jobs is still wanted after ilot stop --all")
	}
}

func TestStopNeedsTheAppsOrAll(t *testing.T) {
	for _, command := range []stopCommand{{Apps: nil, All: false}, {Apps: []string{"api"}, All: true}} {
		if err := command.Run(t.Context()); err == nil {
			t.Errorf("ilot stop with apps %v and --all %v = nil, want an error", command.Apps, command.All)
		}
	}
}

func TestAnUnknownStackOrWorkspaceIsAnError(t *testing.T) {
	shopDir, worktreeDir := shopWithAWorktree(t)
	if err := startInFolder(t, worktreeDir, &readySupervisor{started: map[string]bool{}, dirs: nil}, "api"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(shopDir)

	err := (&stopCommand{Apps: []string{"api"}, All: false, Selection: selection{Workspace: "", Stack: "retry-jbos"}}).Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), `no stack "retry-jbos"`) {
		t.Errorf("ilot stop --stack retry-jbos = %v, want an error that names the stack", err)
	}
	err = (&rmCommand{Stack: "retry-jobs", Selection: workspaceSelection{Workspace: "shpo"}}).Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), `no workspace "shpo"`) || !strings.Contains(err.Error(), "shop") {
		t.Errorf("ilot rm --workspace shpo = %v, want an error that names the known workspaces", err)
	}
}
