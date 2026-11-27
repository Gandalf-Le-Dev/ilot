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
	started []string
}

func (supervisor *readySupervisor) Apply(_ context.Context, specs []ilotctl.ProcessSpec) error {
	for _, spec := range specs {
		supervisor.started = append(supervisor.started, spec.Name)
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
	platformDir := gitRepoAt(t, filepath.Join(codeDir, "platform"), map[string]string{
		"devbox/ilot.yaml": "repos:\n" +
			"  web: {path: " + filepath.Join(codeDir, "web") + ", config: ilot.yaml}\n" +
			"  jobs: {path: " + filepath.Join(codeDir, "jobs") + ", config: ilot.yaml}\n" +
			"apps:\n  api: {run: api, ports: [http], depends_on: [worker]}\n",
	})
	gitRepoAt(t, filepath.Join(codeDir, "web"), map[string]string{"ilot.yaml": "apps:\n  front: {run: front, depends_on: [api]}\n"})
	gitRepoAt(t, filepath.Join(codeDir, "jobs"), map[string]string{"ilot.yaml": "apps:\n  worker: {run: worker}\n"})
	webWorktreeDir := filepath.Join(codeDir, "web-retry")
	runGitInTest(t, filepath.Join(codeDir, "web"), "worktree", "add", "-q", "-b", "PROJ-1-retry", webWorktreeDir)
	homeDir := homeWithPersonalFile(t, "repos:\n  platform: {path: "+platformDir+"}\n")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	git, gitProcessCount := countingGit()

	currentWorkspace, err := workspace.Load(homeDir, git)
	if err != nil {
		t.Fatal(err)
	}
	stackName, _, err := currentWorkspace.StackOf(webWorktreeDir, false, io.Discard)
	if err != nil || stackName != "proj-1" {
		t.Fatalf("StackOf = %q, %v; want proj-1", stackName, err)
	}
	controller, err := newController(currentWorkspace, stackName, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := &readySupervisor{}
	controller.Supervisor = supervisor
	controller.ShellEnv = func() (map[string]string, error) { return map[string]string{}, nil }
	controller.Output = io.Discard
	if err := controller.Start(t.Context(), stackName, []string{"front"}, ilotctl.StartOptions{SkipDependencies: false}); err != nil {
		t.Fatal(err)
	}

	if len(supervisor.started) == 0 {
		t.Fatal("the start reached no process")
	}
	if *gitProcessCount > 5 {
		t.Errorf("ilot start ran %d git processes in a workspace of 3 repos, want 5 at most", *gitProcessCount)
	}
	t.Logf("git processes: %d", *gitProcessCount)
}
