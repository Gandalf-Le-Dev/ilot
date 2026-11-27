package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/state"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

// shopWithAWorktree makes the workspace shop, with a linked worktree on the branch retry-jobs, and an empty state.
func shopWithAWorktree(t *testing.T) (shopDir, worktreeDir string) {
	t.Helper()
	codeDir := canonicalTempDir(t)
	shopDir = gitRepoAt(t, filepath.Join(codeDir, "shop"), map[string]string{"ilot.yaml": "apps:\n  api: {run: api}\n"})
	worktreeDir = filepath.Join(codeDir, "shop-retry")
	runGitInTest(t, shopDir, "worktree", "add", "-q", "-b", "retry-jobs", worktreeDir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return shopDir, worktreeDir
}

// stackOfACommand does what a new ilot command does in a folder: find the workspace, read the bindings, name the stack.
func stackOfACommand(t *testing.T, dir string, recordsBinding bool) (workspace.Workspace, state.Store, workspace.Worktree, string) {
	t.Helper()
	currentWorkspace, worktree, err := workspace.Find(dir, t.TempDir(), testGlobal, workspace.NewGit())
	if err != nil {
		t.Fatal(err)
	}
	currentWorkspace, store, err := withBindings(currentWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	currentWorkspace, stackName, err := stackOfWorktree(store, currentWorkspace, worktree, recordsBinding, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return currentWorkspace, store, worktree, stackName
}

func TestTheFirstStartBindsAWorktreeAndAGitSwitchKeepsItsStack(t *testing.T) {
	_, worktreeDir := shopWithAWorktree(t)

	_, _, _, previewedStackName := stackOfACommand(t, worktreeDir, false)
	runGitInTest(t, worktreeDir, "switch", "-q", "-c", "first-idea")
	_, _, _, stackNameBeforeTheStart := stackOfACommand(t, worktreeDir, false)
	if previewedStackName != "retry-jobs" || stackNameBeforeTheStart != "first-idea" {
		t.Fatalf("stacks before any start = %q then %q; want the branch each time, because only a start binds", previewedStackName, stackNameBeforeTheStart)
	}

	startWorkspace, _, _, stackNameAtTheStart := stackOfACommand(t, worktreeDir, true)
	if stackNameAtTheStart != "first-idea" || startWorkspace.Bindings["first-idea"]["shop"] != worktreeDir {
		t.Fatalf("the first start = %q with %v; want the worktree bound to first-idea", stackNameAtTheStart, startWorkspace.Bindings)
	}

	runGitInTest(t, worktreeDir, "switch", "-q", "-c", "second-idea")
	for _, recordsBinding := range []bool{false, true} {
		laterWorkspace, _, _, laterStackName := stackOfACommand(t, worktreeDir, recordsBinding)
		checkoutByRepo, err := laterWorkspace.CheckoutsForStack(laterStackName)
		if err != nil || laterStackName != "first-idea" || checkoutByRepo["shop"].Dir != worktreeDir {
			t.Errorf("after git switch: stack %q with the checkout %+v, %v; want first-idea with the worktree", laterStackName, checkoutByRepo["shop"], err)
		}
	}
}

func TestACommandThatIsNotAStartReadsTheWorktreeAsIfItWereBound(t *testing.T) {
	_, worktreeDir := shopWithAWorktree(t)
	if err := os.WriteFile(filepath.Join(worktreeDir, "ilot.yaml"), []byte("apps:\n  api: {run: api --retry}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	currentWorkspace, store, _, stackName := stackOfACommand(t, worktreeDir, false)

	stackConfig, err := currentWorkspace.ConfigForStack(stackName)
	if err != nil || stackConfig.Apps["api"].Run != "api --retry" {
		t.Errorf("config of %s = %+v, %v; want the file of the worktree", stackName, stackConfig.Apps, err)
	}
	if recorded, err := loadBindings(store); err != nil || len(recorded) != 0 {
		t.Errorf("recorded bindings = %v, %v; want none before a start", recorded, err)
	}
}

func TestStackBindMovesAWorktreeAndKeepsAStackThatHasApps(t *testing.T) {
	_, worktreeDir := shopWithAWorktree(t)
	currentWorkspace, store, worktree, _ := stackOfACommand(t, worktreeDir, true)
	retryJobs, err := store.Load("retry-jobs")
	if err != nil {
		t.Fatal(err)
	}
	retryJobs.Apps["api"] = state.AppState{Ports: map[string]int{"http": 20000}, Running: false}
	if err := store.Save(retryJobs); err != nil {
		t.Fatal(err)
	}

	_, previousStackName, err := bindWorktree(store, currentWorkspace, worktree, "faster")
	if err != nil || previousStackName != "retry-jobs" {
		t.Fatalf("bindWorktree = %q, %v; want the worktree out of retry-jobs", previousStackName, err)
	}

	if _, _, _, stackName := stackOfACommand(t, worktreeDir, false); stackName != "faster" {
		t.Errorf("stack after ilot stack bind = %q, want faster", stackName)
	}
	retryJobs, err = store.Load("retry-jobs")
	if err != nil || len(retryJobs.BoundWorktrees) != 0 || retryJobs.Apps["api"].Ports["http"] != 20000 {
		t.Errorf("retry-jobs = %+v, %v; want its app kept and no worktree", retryJobs, err)
	}

	currentWorkspace, _, _, _ = stackOfACommand(t, worktreeDir, false)
	if _, _, err := bindWorktree(store, currentWorkspace, worktree, "third"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.StackDir("faster")); err == nil {
		t.Error("the stack faster has no worktree and no app, and its folder is still there")
	}
}

func TestStackBindRefusesASecondWorktreeOfARepoInOneStack(t *testing.T) {
	shopDir, worktreeDir := shopWithAWorktree(t)
	stackOfACommand(t, worktreeDir, true)
	secondWorktreeDir := filepath.Join(filepath.Dir(shopDir), "shop-second")
	runGitInTest(t, shopDir, "worktree", "add", "-q", "-b", "second", secondWorktreeDir)
	currentWorkspace, store, secondWorktree, _ := stackOfACommand(t, secondWorktreeDir, false)

	_, _, err := bindWorktree(store, currentWorkspace, secondWorktree, "retry-jobs")

	if err == nil || !strings.Contains(err.Error(), worktreeDir) || !strings.Contains(err.Error(), "ilot stack bind") {
		t.Errorf("bindWorktree = %v, want an error that names the worktree of the stack and the way out", err)
	}
}

func TestStackShowSaysIfTheWorktreeIsBound(t *testing.T) {
	worktree := workspace.Worktree{Repo: "shop", Dir: "/code/shop-retry", Branch: "second-idea", IsMain: false, MainDir: "/code/shop"}
	checkoutByRepo := map[string]workspace.Worktree{
		"shop": worktree,
		"web":  {Repo: "web", Dir: "/code/web", Branch: "main", IsMain: true, MainDir: "/code/web"},
	}
	var bound, notBound bytes.Buffer

	if err := printStack(&bound, "shop", "retry-jobs", worktree, true, checkoutByRepo); err != nil {
		t.Fatal(err)
	}
	if err := printStack(&notBound, "shop", "second-idea", worktree, false, checkoutByRepo); err != nil {
		t.Fatal(err)
	}

	want := "workspace: shop\nstack: retry-jobs (this worktree is bound to it)\n\n" +
		"REPO  CHECKOUT          BRANCH\n" +
		"shop  /code/shop-retry  second-idea\n" +
		"web   /code/web         main\n"
	if bound.String() != want {
		t.Errorf("ilot stack show =\n%s\nwant\n%s", bound.String(), want)
	}
	if !strings.Contains(notBound.String(), "stack: second-idea (this worktree is not bound yet: the first ilot start here binds it") {
		t.Errorf("ilot stack show of a worktree with no binding =\n%s", notBound.String())
	}
}
