package ilotctl

import (
	"os"
	"path/filepath"
)

func (controller *Controller) binDirIsEmpty(stackName string) bool {
	entries, err := os.ReadDir(filepath.Join(controller.StackDir(stackName), "bin"))
	return err != nil || len(entries) == 0
}
