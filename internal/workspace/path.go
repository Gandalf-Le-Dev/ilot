package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// Canonical returns the absolute path with its symbolic links resolved, so that two paths to one folder compare equal.
// The end of a path that does not exist stays as it is written: a deleted worktree keeps the path that it had.
func Canonical(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	existing, missing := absolutePath, ""
	for {
		resolved, err := filepath.EvalSymlinks(existing)
		if err == nil {
			return filepath.Join(resolved, missing), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolve the path %s: %w", path, err)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return absolutePath, nil
		}
		existing, missing = parent, filepath.Join(filepath.Base(existing), missing)
	}
}
