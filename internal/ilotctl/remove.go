package ilotctl

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

func (controller *Controller) Remove(ctx context.Context, stackName string) (err error) {
	if stackName == workspace.BaseStackName {
		return fmt.Errorf("cannot remove stack %s: ilot needs it, stop its apps with ilot stop", stackName)
	}
	release, err := controller.Store.Lock()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()

	stacks, err := controller.loadSavedStacks()
	if err != nil {
		return err
	}
	_, isKnown := stacks.byName[stackName]
	if !isKnown {
		return fmt.Errorf("no stack %q, see ilot ls", stackName)
	}
	delete(stacks.byName, stackName)
	if err := controller.prepareStacks(stacks); err != nil {
		return err
	}
	if err := controller.saveStacks(stacks); err != nil {
		return err
	}
	if err := controller.applyAll(ctx, stacks); err != nil {
		return err
	}
	return controller.Store.Delete(stackName)
}

// StacksWithoutWorktrees lists the stacks that ilot clean removes: each one whose bound worktrees are all deleted.
// A stack that lost its last worktree to ilot stack bind has none, so it is listed too.
func (controller *Controller) StacksWithoutWorktrees() ([]string, error) {
	allStacks, err := controller.Store.All()
	if err != nil {
		return nil, err
	}
	var stacksWithoutWorktrees []string
	for _, stackState := range allStacks {
		if stackState.Name == workspace.BaseStackName {
			continue
		}
		hasWorktree := false
		for _, worktreeDir := range stackState.BoundWorktrees {
			_, err := os.Stat(worktreeDir)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("look for the worktree of the stack %s: %w", stackState.Name, err)
			}
			hasWorktree = hasWorktree || err == nil
		}
		if !hasWorktree {
			stacksWithoutWorktrees = append(stacksWithoutWorktrees, stackState.Name)
		}
	}
	return stacksWithoutWorktrees, nil
}
