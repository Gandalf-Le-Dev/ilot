package state

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

type StackState struct {
	Name         string              `json:"name"`
	CheckoutDirs map[string]string   `json:"checkout_dirs"`
	Apps         map[string]AppState `json:"apps"`
}

type AppState struct {
	Ports   map[string]int `json:"ports"`
	Running bool           `json:"running"`
}

type Store struct {
	RootDir string
}

func (store Store) StackDir(stackName string) string {
	return filepath.Join(store.RootDir, "stacks", stackName)
}

func (store Store) stateFile(stackName string) string {
	return filepath.Join(store.StackDir(stackName), "state.json")
}

func (store Store) Load(stackName string) (StackState, error) {
	content, err := os.ReadFile(store.stateFile(stackName))
	if errors.Is(err, fs.ErrNotExist) {
		return StackState{Name: stackName, CheckoutDirs: map[string]string{}, Apps: map[string]AppState{}}, nil
	}
	if err != nil {
		return StackState{}, err
	}
	var stackState StackState
	if err := json.Unmarshal(content, &stackState); err != nil {
		return StackState{}, fmt.Errorf("read state of stack %q: %w", stackName, err)
	}
	if stackState.Apps == nil {
		stackState.Apps = map[string]AppState{}
	}
	if stackState.CheckoutDirs == nil {
		stackState.CheckoutDirs = map[string]string{}
	}
	return stackState, nil
}

func (store Store) Save(stackState StackState) error {
	if err := os.MkdirAll(store.StackDir(stackState.Name), 0o700); err != nil {
		return err
	}
	content, err := json.MarshalIndent(stackState, "", "  ")
	if err != nil {
		return err
	}
	temporaryFile := store.stateFile(stackState.Name) + ".tmp"
	if err := os.WriteFile(temporaryFile, append(content, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporaryFile, store.stateFile(stackState.Name))
}

func (store Store) Delete(stackName string) error {
	return os.RemoveAll(store.StackDir(stackName))
}

func (store Store) All() ([]StackState, error) {
	entries, err := os.ReadDir(filepath.Join(store.RootDir, "stacks"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var allStacks []StackState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		stackState, err := store.Load(entry.Name())
		if err != nil {
			return nil, err
		}
		allStacks = append(allStacks, stackState)
	}
	slices.SortFunc(allStacks, func(left, right StackState) int {
		return cmp.Compare(left.Name, right.Name)
	})
	return allStacks, nil
}

func (store Store) Lock() (release func() error, err error) {
	if err := os.MkdirAll(store.RootDir, 0o700); err != nil {
		return nil, err
	}
	lockFile, err := os.OpenFile(filepath.Join(store.RootDir, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return nil, errors.Join(fmt.Errorf("lock %s: %w", lockFile.Name(), err), lockFile.Close())
	}
	return lockFile.Close, nil
}

type workspaceRecord struct {
	Name string `json:"name"`
	Root string `json:"root"`
}

func (store Store) workspaceFile() string {
	return filepath.Join(store.RootDir, "workspace.json")
}

// Claim records the workspace that the state belongs to, and refuses another one.
// The stacks and the processes do not carry the name of their workspace yet, so the base stacks of two workspaces would be one stack.
func (store Store) Claim(workspaceName, rootDir string) error {
	var owner workspaceRecord
	content, err := os.ReadFile(store.workspaceFile())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil {
		if err := json.Unmarshal(content, &owner); err != nil {
			return fmt.Errorf("read %s: %w", store.workspaceFile(), err)
		}
	}
	_, ownerRootErr := os.Stat(owner.Root)
	isOwnerGone := owner.Root == "" || errors.Is(ownerRootErr, fs.ErrNotExist)
	switch {
	case owner.Name == workspaceName && owner.Root == rootDir:
		return nil
	case isOwnerGone || owner.Root == rootDir:
	case owner.Name == workspaceName:
		return fmt.Errorf("two workspaces have the name %q: %s and %s. Set name in the ilot.yaml of one of them", workspaceName, owner.Root, rootDir)
	default:
		return fmt.Errorf("the state in %s belongs to the workspace %q of %s, and this version of ilot keeps one workspace there: run the workspace %q with another XDG_STATE_HOME, or remove the stacks of %q and delete %s", store.RootDir, owner.Name, owner.Root, workspaceName, owner.Name, store.workspaceFile())
	}
	if err := os.MkdirAll(store.RootDir, 0o700); err != nil {
		return err
	}
	content, err = json.Marshal(workspaceRecord{Name: workspaceName, Root: rootDir})
	if err != nil {
		return err
	}
	return os.WriteFile(store.workspaceFile(), append(content, '\n'), 0o600)
}
