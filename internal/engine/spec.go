package engine

import (
	"os"
	"path/filepath"
)

func (executor *Executor) binDirIsEmpty(stackName string) bool {
	entries, err := os.ReadDir(filepath.Join(executor.StackDir(stackName), "bin"))
	return err != nil || len(entries) == 0
}
