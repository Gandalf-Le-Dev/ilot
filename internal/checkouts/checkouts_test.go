package checkouts

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/realpath"
)

func runGit(t *testing.T, dir string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, arguments...)...)
	environmentWithoutGit := slices.DeleteFunc(command.Environ(), func(variable string) bool { return strings.HasPrefix(variable, "GIT_") })
	command.Env = append(environmentWithoutGit, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.net", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.net")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func newRepo(t *testing.T, name string) string {
	t.Helper()
	repoDir := filepath.Join(realpath.Of(t.TempDir()), name)
	runGit(t, filepath.Dir(repoDir), "init", "-q", repoDir)
	runGit(t, repoDir, "commit", "-q", "--allow-empty", "-m", "initial")
	return repoDir
}

func addWorktree(t *testing.T, repoDir, folderName, branch string) string {
	t.Helper()
	worktreeDir := filepath.Join(realpath.Of(t.TempDir()), folderName)
	runGit(t, repoDir, "worktree", "add", "-q", "-b", branch, worktreeDir)
	return worktreeDir
}

func TestForStackPicksTheStackWorktreeElseTheMainCheckout(t *testing.T) {
	platformDir := newRepo(t, "platform")
	portalDir := newRepo(t, "portal")
	platformWorktreeDir := addWorktree(t, platformDir, "example-feature", "PROJ-1234-example-feature")
	addWorktree(t, portalDir, "other-ticket", "TASK-99-other")
	repoPaths := map[string]string{"platform": platformDir, "portal": portalDir}

	checkoutByRepo, err := ForStack(repoPaths, "proj-1234")
	if err != nil {
		t.Fatalf("ForStack: %v", err)
	}
	if got := checkoutByRepo["platform"]; got.Dir != platformWorktreeDir || got.Branch != "PROJ-1234-example-feature" || got.IsMainCheckout {
		t.Errorf("platform checkout = %+v", got)
	}
	if got := checkoutByRepo["portal"]; got.Dir != portalDir || !got.IsMainCheckout {
		t.Errorf("portal checkout = %+v, want its main checkout", got)
	}
}

func TestForStackGivesMainCheckoutsToMasterAndDevbox(t *testing.T) {
	platformDir := newRepo(t, "platform")
	addWorktree(t, platformDir, "master", "PROJ-1-x")
	for _, stackName := range []string{"master", "devbox"} {
		checkoutByRepo, err := ForStack(map[string]string{"platform": platformDir}, stackName)
		if err != nil {
			t.Fatalf("ForStack(%s): %v", stackName, err)
		}
		if got := checkoutByRepo["platform"]; got.Dir != platformDir {
			t.Errorf("ForStack(%s) platform = %+v, want the main checkout", stackName, got)
		}
	}
}

func TestForStackRefusesTwoWorktreesOfOneRepoForATicket(t *testing.T) {
	portalDir := newRepo(t, "portal")
	firstWorktreeDir := addWorktree(t, portalDir, "task-2006-first", "TASK-2006-first")
	secondWorktreeDir := addWorktree(t, portalDir, "task-2006-second", "TASK-2006-second")

	_, err := ForStack(map[string]string{"portal": portalDir}, "task-2006")

	if err == nil || !strings.Contains(err.Error(), firstWorktreeDir) || !strings.Contains(err.Error(), secondWorktreeDir) {
		t.Fatalf("err = %v, want an error naming both worktrees", err)
	}
}

func TestForStackSkipsARepoThatIsNotCloned(t *testing.T) {
	platformDir := newRepo(t, "platform")
	checkoutByRepo, err := ForStack(map[string]string{"platform": platformDir, "portal": filepath.Join(t.TempDir(), "missing")}, "master")
	if err != nil {
		t.Fatalf("ForStack: %v", err)
	}
	if _, isPresent := checkoutByRepo["portal"]; isPresent || len(checkoutByRepo) != 1 {
		t.Errorf("checkouts = %+v, want only platform", checkoutByRepo)
	}
}

func TestRepoOfFindsTheRepoOfAWorktree(t *testing.T) {
	platformDir := newRepo(t, "platform")
	portalDir := newRepo(t, "portal")
	portalWorktreeDir := addWorktree(t, portalDir, "task-1", "TASK-1-x")

	repoName, err := RepoOf(map[string]string{"platform": platformDir, "portal": portalDir}, portalWorktreeDir)
	if err != nil || repoName != "portal" {
		t.Errorf("RepoOf = %q, %v; want portal", repoName, err)
	}
}

func TestRepoOfNamesThePersonalFileKeyForAnUnknownCheckout(t *testing.T) {
	platformDir := newRepo(t, "platform")
	unknownDir := newRepo(t, "acme-portal")

	_, err := RepoOf(map[string]string{"platform": platformDir}, unknownDir)

	if err == nil || !strings.Contains(err.Error(), "repos.") || !strings.Contains(err.Error(), "env.yaml") {
		t.Fatalf("err = %v, want an error naming repos.<name>.path in env.yaml", err)
	}
}

func TestForStackIgnoresAWorktreeWhoseFolderIsDeleted(t *testing.T) {
	platformDir := newRepo(t, "platform")
	deletedWorktreeDir := addWorktree(t, platformDir, "old-attempt", "PROJ-1234-old")
	if err := os.RemoveAll(deletedWorktreeDir); err != nil {
		t.Fatal(err)
	}
	newWorktreeDir := addWorktree(t, platformDir, "new-attempt", "PROJ-1234-new")

	checkoutByRepo, err := ForStack(map[string]string{"platform": platformDir}, "proj-1234")

	if err != nil {
		t.Fatalf("ForStack: %v", err)
	}
	if got := checkoutByRepo["platform"].Dir; got != newWorktreeDir {
		t.Errorf("platform checkout = %q, want the worktree that exists", got)
	}
}

func TestRepoOfFindsTheRepoThroughASymbolicLink(t *testing.T) {
	realParentDir := filepath.Join(t.TempDir(), "devhost-legacy", "legacy")
	if err := os.MkdirAll(realParentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	studioDir := filepath.Join(realParentDir, "studio")
	runGit(t, realParentDir, "init", "-q", studioDir)
	runGit(t, studioDir, "commit", "-q", "--allow-empty", "-m", "initial")
	linkedParentDir := filepath.Join(t.TempDir(), "wd")
	if err := os.Symlink(realParentDir, linkedParentDir); err != nil {
		t.Fatal(err)
	}
	linkedStudioDir := filepath.Join(linkedParentDir, "studio")
	worktreeDir := addWorktree(t, linkedStudioDir, "proj-1-studio", "PROJ-1-studio")

	for _, repoPath := range []string{studioDir, linkedStudioDir} {
		for _, dir := range []string{studioDir, linkedStudioDir, worktreeDir} {
			repoName, err := RepoOf(map[string]string{"studio": repoPath}, dir)
			if err != nil || repoName != "studio" {
				t.Errorf("RepoOf(path %s, dir %s) = %q, %v; want studio", repoPath, dir, repoName, err)
			}
		}
	}
}
