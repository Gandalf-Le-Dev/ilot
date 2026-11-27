package realpath

import "path/filepath"

// Of returns the absolute path of dir with symlinks resolved, so two paths to one folder compare equal.
// A folder that does not exist keeps its cleaned absolute path.
func Of(dir string) string {
	absoluteDir, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	resolvedDir, err := filepath.EvalSymlinks(absoluteDir)
	if err != nil {
		return absoluteDir
	}
	return resolvedDir
}
