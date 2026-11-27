package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestABuildRunsInTheShellOfTheApps(t *testing.T) {
	folder := t.TempDir()

	if err := runBuild(t.Context(), folder, `echo "$0" > shell`, []string{"PATH=/usr/bin:/bin"}); err != nil {
		t.Fatal(err)
	}

	if shell, err := os.ReadFile(filepath.Join(folder, "shell")); err != nil || strings.TrimSpace(string(shell)) != "/bin/sh" {
		t.Errorf("the build ran in %q, %v; want /bin/sh, the shell of the daemon", shell, err)
	}
}
