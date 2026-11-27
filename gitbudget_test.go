package main

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/ilotctl"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

// A supervisor that calls every process ready, for a test of what a command does before it reaches the daemon.
type readySupervisor struct {
	started map[string]bool
}

func (supervisor *readySupervisor) Apply(_ context.Context, specs []ilotctl.ProcessSpec) error {
	for _, spec := range specs {
		supervisor.started[spec.Name] = true
	}
	return nil
}

func (supervisor *readySupervisor) Restart(context.Context, string) error { return nil }

func (supervisor *readySupervisor) Processes(context.Context) (map[string]daemon.Process, error) {
	return map[string]daemon.Process{}, nil
}

func (supervisor *readySupervisor) WaitReady(context.Context, string, time.Duration) error {
	return nil
}

func (supervisor *readySupervisor) IsUp(context.Context) bool { return true }

func (supervisor *readySupervisor) SetRoutes(context.Context, []daemon.Route) error { return nil }

func (supervisor *readySupervisor) Shutdown(context.Context) error { return nil }

func TestStartInAWorkspaceOfThreeReposRunsFiveGitProcessesAtMost(t *testing.T) {
	codeDir := canonicalTempDir(t)
	gitRepoAt(t, filepath.Join(codeDir, "shop"), map[string]string{"ilot.yaml": "repos:\n  web: ../web\n  jobs: ../jobs\nstack:\n  pattern: '[A-Z]+-[0-9]+'\napps:\n  api: {run: api, ports: [http], depends_on: [worker]}\n"})
	gitRepoAt(t, filepath.Join(codeDir, "web"), map[string]string{"ilot.yaml": "member_of: ../shop\napps:\n  front: {run: front, depends_on: [api]}\n"})
	gitRepoAt(t, filepath.Join(codeDir, "jobs"), map[string]string{"ilot.yaml": "member_of: ../shop\napps:\n  worker: {run: worker}\n"})
	webWorktreeDir := filepath.Join(codeDir, "web-retry")
	runGitInTest(t, filepath.Join(codeDir, "web"), "worktree", "add", "-q", "-b", "PROJ-1-retry", webWorktreeDir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	git, gitProcessCount := countingGit()

	currentWorkspace, worktree, err := workspace.Find(webWorktreeDir, t.TempDir(), testGlobal, git)
	if err != nil {
		t.Fatal(err)
	}
	currentWorkspace, store, err := withBindings(currentWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	currentWorkspace, stackName, err := stackOfWorktree(store, currentWorkspace, worktree, true, io.Discard)
	if err != nil || stackName != "proj-1" {
		t.Fatalf("stackOfWorktree = %q, %v; want proj-1", stackName, err)
	}
	controller, err := newController(currentWorkspace, store, stackName, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := &readySupervisor{started: map[string]bool{}}
	controller.Supervisor = supervisor
	controller.ShellEnv = func() (map[string]string, error) { return map[string]string{}, nil }
	controller.Output = io.Discard
	if err := controller.Start(t.Context(), stackName, []string{"front"}, ilotctl.StartOptions{SkipDependencies: false}); err != nil {
		t.Fatal(err)
	}

	if len(supervisor.started) != 3 {
		t.Fatalf("the start reached %v, want front in proj-1, with api and worker in base", supervisor.started)
	}
	if *gitProcessCount > 5 {
		t.Errorf("ilot start ran %d git processes in a workspace of 3 repos, want 5 at most", *gitProcessCount)
	}
	t.Logf("git processes: %d", *gitProcessCount)
}
