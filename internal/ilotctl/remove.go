package ilotctl

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/checkouts"
	"github.com/Gandalf-Le-Dev/ilot/internal/stack"
)

func (controller *Controller) Remove(ctx context.Context, stackName string) (err error) {
	if stackName == stack.MasterStackName || stackName == stack.SharedStackName {
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

func (controller *Controller) StacksWithoutCheckouts() ([]string, error) {
	allStacks, err := controller.Store.All()
	if err != nil {
		return nil, err
	}
	var stacksWithoutCheckouts []string
	for _, stackState := range allStacks {
		if stackState.Name == stack.MasterStackName || stackState.Name == stack.SharedStackName {
			continue
		}
		checkoutByRepo, err := controller.CheckoutsForStack(stackState.Name)
		if checkouts.IsSeveralWorktrees(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(slices.Collect(maps.Values(checkoutByRepo)), isLinkedWorktree) {
			stacksWithoutCheckouts = append(stacksWithoutCheckouts, stackState.Name)
		}
	}
	return stacksWithoutCheckouts, nil
}

func isLinkedWorktree(checkout checkouts.Checkout) bool {
	return !checkout.IsMainCheckout
}
