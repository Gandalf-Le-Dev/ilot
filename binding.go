package main

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/Gandalf-Le-Dev/ilot/internal/state"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

type stackCommand struct {
	Show stackShowCommand `cmd:"" help:"Print the stack of the current worktree, and the checkout of each repo for that stack."`
	Bind stackBindCommand `cmd:"" help:"Put the current worktree in a stack. Use it to give the worktree of a second repo the stack of the first one, or to choose another name than the branch gives."`
}

type stackShowCommand struct{}

type stackBindCommand struct {
	Name string `arg:"" help:"Stack to put the worktree in: lowercase letters, digits and dashes."`
}

func (command *stackShowCommand) Run() error {
	currentWorkspace, worktree, err := findCurrentWorkspace()
	if err != nil {
		return err
	}
	currentWorkspace, _, err = withBindings(currentWorkspace)
	if err != nil {
		return err
	}
	stackName, isBound, err := currentWorkspace.StackOf(worktree)
	if err != nil {
		return err
	}
	if !isBound {
		// The checkouts below are the ones that the first start gives.
		if currentWorkspace.Bindings, _, err = currentWorkspace.Bindings.Bind(stackName, worktree); err != nil {
			return err
		}
	}
	checkoutByRepo, err := currentWorkspace.CheckoutsForStack(stackName)
	if err != nil {
		return err
	}
	return printStack(os.Stdout, currentWorkspace.Name, stackName, worktree, isBound, checkoutByRepo)
}

func printStack(output io.Writer, workspaceName, stackName string, worktree workspace.Worktree, isBound bool, checkoutByRepo map[string]workspace.Worktree) error {
	binding := "this worktree is bound to it"
	switch {
	case worktree.IsMain:
		binding = "the main checkout of every repo"
	case !isBound:
		binding = "this worktree is not bound yet: the first ilot start here binds it, and ilot stack bind <name> chooses another stack"
	}
	fmt.Fprintf(output, "workspace: %s\nstack: %s (%s)\n\n", workspaceName, stackName, binding)
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "REPO\tCHECKOUT\tBRANCH")
	for _, repoName := range slices.Sorted(maps.Keys(checkoutByRepo)) {
		checkout := checkoutByRepo[repoName]
		fmt.Fprintf(table, "%s\t%s\t%s\n", repoName, checkout.Dir, checkout.Branch)
	}
	return table.Flush()
}

func (command *stackBindCommand) Run() error {
	currentWorkspace, worktree, err := findCurrentWorkspace()
	if err != nil {
		return err
	}
	currentWorkspace, store, err := withBindings(currentWorkspace)
	if err != nil {
		return err
	}
	_, previousStackName, err := bindWorktree(store, currentWorkspace, worktree, command.Name)
	if err != nil {
		return err
	}
	fmt.Printf("the worktree %s now belongs to the stack %s\n", worktree.Dir, command.Name)
	if previousStackName == "" {
		return nil
	}
	previousStack, err := store.Load(previousStackName)
	if err != nil {
		return err
	}
	if len(previousStack.Apps) > 0 {
		fmt.Printf("the stack %s keeps its apps and their ports: remove it with ilot rm %s\n", previousStackName, previousStackName)
	}
	return nil
}

// withBindings gives the workspace the worktrees of each stack, which the state holds.
func withBindings(currentWorkspace workspace.Workspace) (workspace.Workspace, state.Store, error) {
	store := state.Store{RootDir: stateDirOf(currentWorkspace.HomeDir)}
	if err := store.Claim(currentWorkspace.Name, currentWorkspace.RootDir); err != nil {
		return workspace.Workspace{}, state.Store{}, err
	}
	bindings, err := loadBindings(store)
	if err != nil {
		return workspace.Workspace{}, state.Store{}, err
	}
	currentWorkspace.Bindings = bindings
	return currentWorkspace, store, nil
}

func loadBindings(store state.Store) (workspace.Bindings, error) {
	allStacks, err := store.All()
	if err != nil {
		return nil, err
	}
	bindings := workspace.Bindings{}
	for _, stackState := range allStacks {
		if len(stackState.BoundWorktrees) > 0 {
			bindings[stackState.Name] = stackState.BoundWorktrees
		}
	}
	return bindings, nil
}

// stackOfWorktree gives the stack of a worktree. A worktree with no binding gets the name that its branch gives:
// ilot start records that binding, and another command only works as if it were recorded.
func stackOfWorktree(store state.Store, currentWorkspace workspace.Workspace, worktree workspace.Worktree, recordsBinding bool, notes io.Writer) (workspace.Workspace, string, error) {
	stackName, isBound, err := currentWorkspace.StackOf(worktree)
	if err != nil || isBound {
		return currentWorkspace, stackName, err
	}
	if !recordsBinding {
		currentWorkspace.Bindings, _, err = currentWorkspace.Bindings.Bind(stackName, worktree)
		return currentWorkspace, stackName, err
	}
	currentWorkspace, _, err = bindWorktree(store, currentWorkspace, worktree, stackName)
	if err != nil {
		return workspace.Workspace{}, "", err
	}
	fmt.Fprintf(notes, "the worktree %s now belongs to the stack %s: ilot stack bind <name> gives it another stack\n", worktree.Dir, stackName)
	return currentWorkspace, stackName, nil
}

// bindWorktree records in the state that a worktree belongs to a stack. It gives the stack that the worktree leaves.
func bindWorktree(store state.Store, currentWorkspace workspace.Workspace, worktree workspace.Worktree, stackName string) (bound workspace.Workspace, previousStackName string, err error) {
	release, err := store.Lock()
	if err != nil {
		return workspace.Workspace{}, "", err
	}
	defer func() { err = errors.Join(err, release()) }()

	// Another command can have bound a worktree since this command read the state.
	recordedBindings, err := loadBindings(store)
	if err != nil {
		return workspace.Workspace{}, "", err
	}
	newBindings, previousStackName, err := recordedBindings.Bind(stackName, worktree)
	if err != nil {
		return workspace.Workspace{}, "", err
	}
	for _, changedStackName := range []string{previousStackName, stackName} {
		if changedStackName == "" {
			continue
		}
		stackState, err := store.Load(changedStackName)
		if err != nil {
			return workspace.Workspace{}, "", err
		}
		stackState.BoundWorktrees = newBindings[changedStackName]
		if len(stackState.BoundWorktrees) == 0 && len(stackState.Apps) == 0 {
			// A stack with no worktree and no app is only a name.
			if err := store.Delete(changedStackName); err != nil {
				return workspace.Workspace{}, "", err
			}
			continue
		}
		if err := store.Save(stackState); err != nil {
			return workspace.Workspace{}, "", err
		}
	}
	currentWorkspace.Bindings = newBindings
	return currentWorkspace, previousStackName, nil
}
