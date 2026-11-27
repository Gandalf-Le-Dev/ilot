package ilotctl

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
)

func (controller *Controller) Remove(ctx context.Context, stackName string) (err error) {
	if stackName == plan.BaseStack {
		return fmt.Errorf("cannot remove stack %s: ilot needs it, stop its apps with ilot stop", stackName)
	}
	release, err := controller.Store.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()

	current, err := controller.loadState()
	if err != nil {
		return err
	}
	if _, isKnown := current.Stacks(controller.Workspace)[stackName]; !isKnown {
		return fmt.Errorf("no stack %q, see ilot ls", stackName)
	}
	current.RemoveStack(controller.Workspace, stackName)
	snapshot, problems := controller.snapshotOf(current, plan.BaseStack)
	if err := errors.Join(problems...); err != nil {
		return err
	}
	if err := controller.deriveCheckouts(snapshot); err != nil {
		return err
	}
	controller.prepareStacks(snapshot)
	if err := controller.saveStacks(snapshot); err != nil {
		return err
	}
	if err := controller.applyAll(ctx, snapshot); err != nil {
		return err
	}
	return os.RemoveAll(controller.StackDir(stackName))
}

// StacksWithoutWorktrees lists the stacks that ilot clean removes: each one whose bound worktrees are all deleted.
// A stack that lost its last worktree to ilot stack bind has none, so it is listed too.
func (controller *Controller) StacksWithoutWorktrees() ([]string, error) {
	current, err := controller.loadState()
	if err != nil {
		return nil, err
	}
	var stacksWithoutWorktrees []string
	stacks := current.Stacks(controller.Workspace)
	for _, stackName := range slices.Sorted(maps.Keys(stacks)) {
		if stackName == plan.BaseStack {
			continue
		}
		hasWorktree := false
		for _, worktreeDir := range stacks[stackName].Checkouts {
			_, err := os.Stat(worktreeDir)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("look for the worktree of the stack %s: %w", stackName, err)
			}
			hasWorktree = hasWorktree || err == nil
		}
		if !hasWorktree {
			stacksWithoutWorktrees = append(stacksWithoutWorktrees, stackName)
		}
	}
	return stacksWithoutWorktrees, nil
}
