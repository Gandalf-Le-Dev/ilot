package workspace

import (
	"maps"
	"os"
	"strings"
	"testing"
)

func TestAGitSwitchAfterTheBindingKeepsTheStack(t *testing.T) {
	shopDir := newRepo(t, "shop")
	worktreeDir := addWorktree(t, shopDir, "shop-retry", "retry-jobs")
	workspace := Workspace{Name: "shop", RepoPaths: map[string]string{"shop": shopDir}, git: NewGit()}
	locate := func() Worktree {
		t.Helper()
		located, err := NewGit().Locate(worktreeDir)
		if err != nil {
			t.Fatal(err)
		}
		located.Repo = "shop"
		return located
	}

	stackName, isBound, err := workspace.StackOf(locate())
	if err != nil || isBound || stackName != "retry-jobs" {
		t.Fatalf("StackOf before the binding = %q, %t, %v; want retry-jobs from the branch, not bound", stackName, isBound, err)
	}
	workspace.Bindings, _, err = workspace.Bindings.Bind(stackName, locate())
	if err != nil {
		t.Fatal(err)
	}

	gitInTest(t, worktreeDir, "switch", "-q", "-c", "another-idea")
	if stackName, isBound, err := workspace.StackOf(locate()); err != nil || !isBound || stackName != "retry-jobs" {
		t.Errorf("StackOf after git switch = %q, %t, %v; want retry-jobs, bound", stackName, isBound, err)
	}
	gitInTest(t, worktreeDir, "checkout", "-q", "--detach")
	if stackName, isBound, err := workspace.StackOf(locate()); err != nil || !isBound || stackName != "retry-jobs" {
		t.Errorf("StackOf with a detached HEAD = %q, %t, %v; want retry-jobs, bound", stackName, isBound, err)
	}
}

func TestAMainCheckoutIsTheBaseStackWithNoBinding(t *testing.T) {
	stackName, isBound, err := Workspace{}.StackOf(Worktree{Repo: "shop", Dir: "/code/shop", Branch: "retry-jobs", IsMain: true})
	if err != nil || !isBound || stackName != "base" {
		t.Errorf("StackOf(main checkout) = %q, %t, %v; want base", stackName, isBound, err)
	}
}

func TestBindMovesAWorktreeToAnotherStack(t *testing.T) {
	shopWorktree := Worktree{Repo: "shop", Dir: "/code/shop-retry", Branch: "retry-jobs"}
	bindings := Bindings{"retry-jobs": {"shop": "/code/shop-retry", "web": "/code/web-retry"}, "solo": {"shop": "/code/shop-solo"}}
	before := maps.Clone(bindings["retry-jobs"])

	bound, previousStackName, err := bindings.Bind("faster", shopWorktree)

	if err != nil || previousStackName != "retry-jobs" {
		t.Fatalf("Bind = %v, %q, %v; want the worktree out of retry-jobs", bound, previousStackName, err)
	}
	if stackName, isBound := bound.StackOf(shopWorktree); !isBound || stackName != "faster" {
		t.Errorf("StackOf after the move = %q, %t; want faster", stackName, isBound)
	}
	if len(bound["retry-jobs"]) != 1 || bound["retry-jobs"]["web"] != "/code/web-retry" || bound["solo"]["shop"] != "/code/shop-solo" {
		t.Errorf("bindings = %v, want the other worktrees where they were", bound)
	}
	if !maps.Equal(bindings["retry-jobs"], before) {
		t.Errorf("Bind changed the bindings that it got: %v", bindings)
	}

	bound, previousStackName, err = bound.Bind("solo-2", Worktree{Repo: "shop", Dir: "/code/shop-solo"})
	if _, hasEmptyStack := bound["solo"]; err != nil || previousStackName != "solo" || hasEmptyStack {
		t.Errorf("Bind of the last worktree of a stack = %v, %q, %v; want the stack solo gone", bound, previousStackName, err)
	}
}

func TestBindPutsTheWorktreeOfASecondRepoInAnExistingStack(t *testing.T) {
	bindings := Bindings{"retry-jobs": {"shop": "/code/shop-retry"}}

	bound, previousStackName, err := bindings.Bind("retry-jobs", Worktree{Repo: "web", Dir: "/code/web-wt7", Branch: "tool-made-name"})

	if err != nil || previousStackName != "" || bound["retry-jobs"]["web"] != "/code/web-wt7" || bound["retry-jobs"]["shop"] != "/code/shop-retry" {
		t.Errorf("Bind = %v, %q, %v; want both worktrees in retry-jobs", bound, previousStackName, err)
	}
}

func TestBindRefusesWhatCannotBeAStack(t *testing.T) {
	existingWorktreeDir := t.TempDir()
	bindings := Bindings{"retry-jobs": {"shop": existingWorktreeDir}}
	testCases := []struct {
		name            string
		stackName       string
		worktree        Worktree
		wantErrorSubstr string
	}{
		{name: "a main checkout", stackName: "retry-jobs", worktree: Worktree{Repo: "shop", Dir: "/code/shop", IsMain: true}, wantErrorSubstr: "main checkout"},
		{name: "the base stack", stackName: "base", worktree: Worktree{Repo: "shop", Dir: "/code/shop-x"}, wantErrorSubstr: "the base stack is the main checkout of every repo"},
		{name: "a name with capitals", stackName: "Retry_Jobs", worktree: Worktree{Repo: "shop", Dir: "/code/shop-x"}, wantErrorSubstr: "lowercase letters, digits and dashes"},
		{name: "no name", stackName: "", worktree: Worktree{Repo: "shop", Dir: "/code/shop-x"}, wantErrorSubstr: "lowercase letters, digits and dashes"},
		{name: "a second worktree of a repo", stackName: "retry-jobs", worktree: Worktree{Repo: "shop", Dir: "/code/shop-x"}, wantErrorSubstr: "a stack takes one worktree of a repo"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, err := bindings.Bind(testCase.stackName, testCase.worktree)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErrorSubstr) {
				t.Errorf("Bind = %v, want an error with %q", err, testCase.wantErrorSubstr)
			}
		})
	}
}

func TestBindReplacesABoundWorktreeWhoseFolderIsDeleted(t *testing.T) {
	deletedWorktreeDir := t.TempDir()
	if err := os.Remove(deletedWorktreeDir); err != nil {
		t.Fatal(err)
	}
	bindings := Bindings{"retry-jobs": {"shop": deletedWorktreeDir}}

	bound, _, err := bindings.Bind("retry-jobs", Worktree{Repo: "shop", Dir: "/code/shop-second-attempt"})

	if err != nil || bound["retry-jobs"]["shop"] != "/code/shop-second-attempt" {
		t.Errorf("Bind = %v, %v; want the new worktree in the place of the deleted one", bound, err)
	}
}
