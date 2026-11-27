package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/realpath"
)

func gitRepoWithFiles(t *testing.T, contentByPath map[string]string) string {
	t.Helper()
	repoDir := realpath.Of(t.TempDir())
	for relativePath, content := range contentByPath {
		filePath := filepath.Join(repoDir, relativePath)
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, arguments := range [][]string{
		{"init", "-q", "-b", "master"},
		{"add", "-A"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		command := exec.Command("git", arguments...)
		command.Dir = repoDir
		environmentWithoutGit := slices.DeleteFunc(command.Environ(), func(variable string) bool { return strings.HasPrefix(variable, "GIT_") })
		command.Env = append(environmentWithoutGit, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.net", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.net")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	return repoDir
}

func homeWithPersonalFile(t *testing.T, content string) string {
	t.Helper()
	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, ".config", "ilot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeDir, personalSettingsPath), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return homeDir
}

func TestARepoCheckoutWithoutItsConfigFileDeclaresNoApps(t *testing.T) {
	portalDir := gitRepoWithFiles(t, map[string]string{"README.md": "no ilot.yaml on this branch\n"})
	platformDir := gitRepoWithFiles(t, map[string]string{
		"devbox/ilot.yaml": "domain: dev.example.net\nrepos:\n  portal: {path: /nowhere, config: ilot.yaml}\napps:\n  standalone: {run: standalone}\n",
	})
	homeDir := homeWithPersonalFile(t, "repos:\n  platform: {path: "+platformDir+"}\n  portal: {path: "+portalDir+"}\n")
	currentWorkspace, err := loadWorkspace(homeDir)
	if err != nil {
		t.Fatalf("loadWorkspace: %v", err)
	}

	config, err := currentWorkspace.configForStack("master")

	if err != nil {
		t.Fatalf("configForStack: %v", err)
	}
	if _, isKnown := config.Apps["standalone"]; !isKnown {
		t.Errorf("apps = %v, want platform's apps", config.Apps)
	}
}

func TestPlatformWithoutItsConfigNamesThePersonalSetting(t *testing.T) {
	homeDir := homeWithPersonalFile(t, "repos:\n  platform: {path: "+t.TempDir()+"}\n")

	_, err := loadWorkspace(homeDir)

	if err == nil || !strings.Contains(err.Error(), "repos.platform.path") {
		t.Fatalf("loadWorkspace = %v, want an error naming repos.platform.path", err)
	}
}

func TestAPersonalRepoWithPathAndConfigJoinsTheWorkspace(t *testing.T) {
	toolDir := gitRepoWithFiles(t, map[string]string{"ilot.yaml": "apps:\n  mytool-daemon: {run: mytool daemon}\n"})
	platformDir := gitRepoWithFiles(t, map[string]string{
		"devbox/ilot.yaml": "domain: dev.example.net\napps:\n  standalone: {run: standalone}\n",
	})
	homeDir := homeWithPersonalFile(t, "repos:\n  platform: {path: "+platformDir+"}\n  mytool: {path: "+toolDir+", config: ilot.yaml}\n  pathonly: {path: "+t.TempDir()+"}\n")
	currentWorkspace, err := loadWorkspace(homeDir)
	if err != nil {
		t.Fatalf("loadWorkspace: %v", err)
	}
	if _, isKnown := currentWorkspace.repoPaths["pathonly"]; isKnown {
		t.Errorf("a personal repo without a config must stay unknown: %v", currentWorkspace.repoPaths)
	}

	config, err := currentWorkspace.configForStack("master")

	if err != nil {
		t.Fatalf("configForStack: %v", err)
	}
	if _, isKnown := config.Apps["mytool-daemon"]; !isKnown {
		t.Errorf("apps = %v, want the personal repo's apps", config.Apps)
	}
	if _, isKnown := config.Apps["standalone"]; !isKnown {
		t.Errorf("apps = %v, want platform's apps too", config.Apps)
	}
}
