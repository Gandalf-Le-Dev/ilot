package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The README sends a reader to examples/, so its files must pass ilot check as the README says to copy them.
func TestTheSampleWorkspacePassesCheck(t *testing.T) {
	codeDir := canonicalTempDir(t)
	for _, repoName := range []string{"shop", "web"} {
		repoDir := filepath.Join(codeDir, repoName)
		if err := os.CopyFS(repoDir, os.DirFS(filepath.Join("examples", repoName))); err != nil {
			t.Fatal(err)
		}
		runGitInTest(t, repoDir, "init", "-q", "-b", "main")
		runGitInTest(t, repoDir, "add", "-A")
		runGitInTest(t, repoDir, "commit", "-q", "-m", "sample workspace")
	}
	t.Setenv("HOME", canonicalTempDir(t))
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	for _, repoName := range []string{"shop", "web"} {
		t.Chdir(filepath.Join(codeDir, repoName))
		if _, _, err := captureOutput(t, (&checkCommand{Path: ""}).Run); err != nil {
			t.Errorf("ilot check in examples/%s: %v", repoName, err)
		}
	}
}
