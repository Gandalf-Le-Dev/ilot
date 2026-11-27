package workspace

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalResolvesALinkInThePath(t *testing.T) {
	realDir := canonicalTempDir(t)
	linkedDir := filepath.Join(canonicalTempDir(t), "link")
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Fatal(err)
	}

	got, err := Canonical(linkedDir)

	if err != nil || got != realDir {
		t.Errorf("Canonical(%s) = %q, %v; want %q", linkedDir, got, err, realDir)
	}
}

func TestCanonicalKeepsTheEndOfAPathThatDoesNotExist(t *testing.T) {
	realDir := canonicalTempDir(t)
	linkedDir := filepath.Join(canonicalTempDir(t), "link")
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Fatal(err)
	}

	got, err := Canonical(filepath.Join(linkedDir, "deleted", "worktree"))

	if want := filepath.Join(realDir, "deleted", "worktree"); err != nil || got != want {
		t.Errorf("Canonical of a missing folder behind a link = %q, %v; want %q", got, err, want)
	}
}

func TestCanonicalReportsAPathThatItCannotResolve(t *testing.T) {
	loopDir := canonicalTempDir(t)
	first, second := filepath.Join(loopDir, "first"), filepath.Join(loopDir, "second")
	if err := os.Symlink(first, second); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, first); err != nil {
		t.Fatal(err)
	}

	got, err := Canonical(filepath.Join(first, "repo"))

	if err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Canonical of a link that points to itself = %q, %v; want the error of the loop", got, err)
	}
}
