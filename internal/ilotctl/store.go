package ilotctl

import (
	"context"
	"maps"
	"os"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/state"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

// StackState is one stack of the workspace, as the Controller works on it.
type StackState struct {
	Name string
	// The checkout of every repo, which a command derives from the binding. The state file keeps only the binding.
	CheckoutDirs   map[string]string
	BoundWorktrees map[string]string
	Apps           map[string]AppState
}

type AppState struct {
	Ports   map[string]int
	Running bool
}

// StackStore reads and writes the stacks of one workspace in the state file of every workspace.
type StackStore struct {
	Files     state.Store
	Workspace string
	// The main checkout of the root repo of the workspace.
	Root string
}

func (store StackStore) StackDir(stackName string) string {
	return store.Files.StackDir(store.Workspace, stackName)
}

func (store StackStore) Lock() (release func() error, err error) {
	return store.Files.Lock(context.Background())
}

func (store StackStore) All() ([]StackState, error) {
	current, err := store.Files.Load()
	if err != nil {
		return nil, err
	}
	var allStacks []StackState
	for _, stackName := range slices.Sorted(maps.Keys(current.Stacks(store.Workspace))) {
		allStacks = append(allStacks, store.stackOf(current, stackName))
	}
	return allStacks, nil
}

func (store StackStore) Load(stackName string) (StackState, error) {
	current, err := store.Files.Load()
	if err != nil {
		return StackState{}, err
	}
	return store.stackOf(current, stackName), nil
}

// The checkout of each repo comes from the binding when a command loads the stack, so a deleted worktree never leaves a stale folder.
func (store StackStore) stackOf(current state.State, stackName string) StackState {
	stackState := StackState{Name: stackName, CheckoutDirs: map[string]string{}, BoundWorktrees: nil, Apps: map[string]AppState{}}
	saved, isSaved := current.Stacks(store.Workspace)[stackName]
	if !isSaved {
		return stackState
	}
	if stackName != workspace.BaseStackName && len(saved.Checkouts) > 0 {
		stackState.BoundWorktrees = maps.Clone(saved.Checkouts)
	}
	for appName, app := range saved.Apps {
		stackState.Apps[appName] = AppState{Ports: maps.Clone(app.Ports), Running: app.Wanted}
	}
	return stackState
}

func (store StackStore) Save(stackState StackState) error {
	return store.SaveAll([]StackState{stackState})
}

func (store StackStore) SaveAll(stackStates []StackState) error {
	current, err := store.Files.Load()
	if err != nil {
		return err
	}
	if err := current.Claim(store.Workspace, store.Root); err != nil {
		return err
	}
	for _, stackState := range stackStates {
		saved := current.Stack(store.Workspace, stackState.Name)
		// The base stack has no binding: its checkouts are the main checkouts.
		saved.Checkouts = map[string]string{}
		if stackState.Name != workspace.BaseStackName {
			saved.Checkouts = maps.Clone(stackState.BoundWorktrees)
		}
		saved.Apps = map[string]state.App{}
		for appName, app := range stackState.Apps {
			saved.Apps[appName] = state.App{Wanted: app.Running, Ports: maps.Clone(app.Ports), Route: nil, Container: ""}
		}
	}
	return store.Files.Save(current)
}

func (store StackStore) Delete(stackName string) error {
	current, err := store.Files.Load()
	if err != nil {
		return err
	}
	current.RemoveStack(store.Workspace, stackName)
	if err := store.Files.Save(current); err != nil {
		return err
	}
	return os.RemoveAll(store.StackDir(stackName))
}
