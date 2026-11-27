package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func countingGit() (*Git, *int) {
	git := NewGit()
	processCount := 0
	run := git.Run
	git.Run = func(dir string, arguments ...string) (string, error) {
		processCount++
		return run(dir, arguments...)
	}
	return git, &processCount
}

func TestGitAsksTheSameQuestionOneTime(t *testing.T) {
	repoDir := newRepo(t, "shop")
	git, processCount := countingGit()

	for range 3 {
		if _, err := git.Worktrees("shop", repoDir); err != nil {
			t.Fatal(err)
		}
		if _, err := git.Locate(repoDir); err != nil {
			t.Fatal(err)
		}
	}

	if *processCount != 2 {
		t.Errorf("git ran %d times, want 1 for the worktree list and 1 for the location", *processCount)
	}
}

func TestGitKeepsAFailureToo(t *testing.T) {
	git, processCount := countingGit()
	notARepo := canonicalTempDir(t)

	for range 2 {
		if _, err := git.Locate(notARepo); err == nil {
			t.Fatal("Locate outside a repo gave no error")
		}
	}

	if *processCount != 1 {
		t.Errorf("git ran %d times, want 1", *processCount)
	}
}

func TestGitIgnoresTheGitEnvironmentOfAnEnclosingHook(t *testing.T) {
	decoyRepoDir := newRepo(t, "decoy")
	mainCheckoutDir := newRepo(t, "shop")
	t.Setenv("GIT_DIR", filepath.Join(decoyRepoDir, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoyRepoDir, ".git", "index"))

	located, err := NewGit().Locate(mainCheckoutDir)

	if err != nil || located.Dir != mainCheckoutDir {
		t.Errorf("Locate with GIT_DIR set = %+v, %v; want the repo of the folder, %s", located, err, mainCheckoutDir)
	}
	decoyConfig, err := os.ReadFile(filepath.Join(decoyRepoDir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(decoyConfig), "worktree") {
		t.Errorf("the git calls wrote into the repo of GIT_DIR:\n%s", decoyConfig)
	}
}
