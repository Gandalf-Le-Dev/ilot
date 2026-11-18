package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func runGitInTest(t *testing.T, dir string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = dir
	environmentWithoutGit := slices.DeleteFunc(command.Environ(), func(variable string) bool { return strings.HasPrefix(variable, "GIT_") })
	command.Env = append(environmentWithoutGit, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.net", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.net")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func workspaceWithPlatform(t *testing.T, platformPath string) workspace {
	t.Helper()
	homeDir := homeWithPersonalFile(t, "repos:\n  platform: {path: "+platformPath+"}\n")
	currentWorkspace, err := loadWorkspace(homeDir)
	if err != nil {
		t.Fatal(err)
	}
	return currentWorkspace
}

func platformRepoForTest(t *testing.T) string {
	t.Helper()
	return gitRepoWithFiles(t, map[string]string{"devbox/ilot.yaml": "domain: dev.example.net\napps:\n  standalone: {run: standalone}\n"})
}

func TestCrossStackCommandsUseTheMasterStackFromAnyFolder(t *testing.T) {
	platformDir := platformRepoForTest(t)
	currentWorkspace := workspaceWithPlatform(t, platformDir)
	var notes bytes.Buffer

	worktree, err := worktreeForCommand(currentWorkspace, t.TempDir(), true, &notes)

	if err != nil || worktree.Name != "master" || worktree.WorktreeDir != platformDir {
		t.Fatalf("worktreeForCommand = %+v, %v; want the master stack of %s", worktree, err, platformDir)
	}
	if notes.Len() != 0 {
		t.Errorf("notes = %q, want none for a command that works across stacks", notes.String())
	}
}

func TestStartOutsideACheckoutUsesMasterAndSaysSo(t *testing.T) {
	platformDir := platformRepoForTest(t)
	currentWorkspace := workspaceWithPlatform(t, platformDir)
	var notes bytes.Buffer

	worktree, err := worktreeForCommand(currentWorkspace, t.TempDir(), false, &notes)

	if err != nil || worktree.Name != "master" {
		t.Fatalf("worktreeForCommand = %+v, %v; want the master stack", worktree, err)
	}
	if !strings.Contains(notes.String(), "not in a checkout") {
		t.Errorf("notes = %q, want the master stack note", notes.String())
	}
}

func TestStartInAnUnknownRepoKeepsTheHint(t *testing.T) {
	platformDir := platformRepoForTest(t)
	currentWorkspace := workspaceWithPlatform(t, platformDir)
	unknownRepoDir := gitRepoWithFiles(t, map[string]string{"README.md": "not listed\n"})
	var notes bytes.Buffer

	_, err := worktreeForCommand(currentWorkspace, unknownRepoDir, false, &notes)

	if err == nil || !strings.Contains(err.Error(), "repos.") {
		t.Fatalf("worktreeForCommand = %v, want the error that names repos.<name>.path", err)
	}
}

func TestCrossStackCommandsIgnoreALinkedPlatformPath(t *testing.T) {
	platformDir := platformRepoForTest(t)
	linkedWorktreeDir := filepath.Join(t.TempDir(), "proj-1234-work")
	runGitInTest(t, platformDir, "worktree", "add", "-q", "-b", "PROJ-1234-work", linkedWorktreeDir)
	currentWorkspace := workspaceWithPlatform(t, linkedWorktreeDir)
	var notes bytes.Buffer

	worktree, err := worktreeForCommand(currentWorkspace, t.TempDir(), true, &notes)

	if err != nil || worktree.Name != "master" || worktree.WorktreeDir != platformDir {
		t.Fatalf("worktreeForCommand = %+v, %v; want master from the main checkout %s", worktree, err, platformDir)
	}
}

func TestStartFromAHomeFolderThatIsACheckoutUsesMasterAndSaysSo(t *testing.T) {
	platformDir := platformRepoForTest(t)
	currentWorkspace := workspaceWithPlatform(t, platformDir)
	runGitInTest(t, currentWorkspace.homeDir, "init", "-q", "-b", "master")
	runGitInTest(t, currentWorkspace.homeDir, "commit", "-q", "--allow-empty", "-m", "dotfiles")
	var notes bytes.Buffer

	worktree, err := worktreeForCommand(currentWorkspace, currentWorkspace.homeDir, false, &notes)

	if err != nil || worktree.Name != "master" || worktree.WorktreeDir != platformDir {
		t.Fatalf("worktreeForCommand = %+v, %v; want the master stack of %s", worktree, err, platformDir)
	}
	if !strings.Contains(notes.String(), "home folder") {
		t.Errorf("notes = %q, want the home folder note", notes.String())
	}
}

func TestStartFromAHomeCheckoutReachedThroughALinkUsesMaster(t *testing.T) {
	platformDir := platformRepoForTest(t)
	homeDir := homeWithPersonalFile(t, "repos:\n  platform: {path: "+platformDir+"}\n")
	runGitInTest(t, homeDir, "init", "-q", "-b", "master")
	runGitInTest(t, homeDir, "commit", "-q", "--allow-empty", "-m", "dotfiles")
	linkedHomeDir := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(homeDir, linkedHomeDir); err != nil {
		t.Fatal(err)
	}
	currentWorkspace, err := loadWorkspace(linkedHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	var notes bytes.Buffer

	worktree, err := worktreeForCommand(currentWorkspace, linkedHomeDir, false, &notes)

	if err != nil || worktree.Name != "master" || worktree.WorktreeDir != platformDir {
		t.Fatalf("worktreeForCommand = %+v, %v; want the master stack of %s", worktree, err, platformDir)
	}
}
