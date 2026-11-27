package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := Canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func gitInTest(t *testing.T, dir string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, arguments...)...)
	environmentWithoutGit := slices.DeleteFunc(command.Environ(), func(variable string) bool { return strings.HasPrefix(variable, "GIT_") })
	command.Env = append(environmentWithoutGit, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.net", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.net")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func newRepoAt(t *testing.T, repoDir string, contentByPath map[string]string) string {
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
	gitInTest(t, repoDir, "init", "-q", "-b", "main")
	gitInTest(t, repoDir, "add", "-A")
	gitInTest(t, repoDir, "commit", "-q", "--allow-empty", "-m", "initial")
	return repoDir
}

func newRepo(t *testing.T, name string) string {
	t.Helper()
	return newRepoAt(t, filepath.Join(canonicalTempDir(t), name), nil)
}

func addWorktree(t *testing.T, repoDir, folderName, branch string) string {
	t.Helper()
	worktreeDir := filepath.Join(canonicalTempDir(t), folderName)
	gitInTest(t, repoDir, "worktree", "add", "-q", "-b", branch, worktreeDir)
	return worktreeDir
}

func workspaceOfRepos(repoPaths map[string]string) Workspace {
	return Workspace{Name: "platform", RootDir: repoPaths["platform"], HomeDir: "", RepoPaths: repoPaths, StackPattern: regexp.MustCompile(ticketPattern), git: NewGit()}
}
