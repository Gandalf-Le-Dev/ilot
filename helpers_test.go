package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
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

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := workspace.Canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func gitRepoAt(t *testing.T, repoDir string, contentByPath map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for relativePath, content := range contentByPath {
		filePath := filepath.Join(repoDir, relativePath)
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGitInTest(t, repoDir, "init", "-q", "-b", "main")
	runGitInTest(t, repoDir, "add", "-A")
	runGitInTest(t, repoDir, "commit", "-q", "--allow-empty", "-m", "init")
	return repoDir
}

func gitRepoWithFiles(t *testing.T, contentByPath map[string]string) string {
	t.Helper()
	return gitRepoAt(t, canonicalTempDir(t), contentByPath)
}

func homeWithPersonalFile(t *testing.T, content string) string {
	t.Helper()
	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, ".config", "ilot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeDir, workspace.PersonalSettingsPath), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return homeDir
}

func countingGit() (*workspace.Git, *int) {
	git := workspace.NewGit()
	processCount := 0
	run := git.Run
	git.Run = func(dir string, arguments ...string) (string, error) {
		processCount++
		return run(dir, arguments...)
	}
	return git, &processCount
}
