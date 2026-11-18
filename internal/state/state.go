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
	Name              string            `json:"name"`
	CheckoutDirs      map[string]string `json:"checkout_dirs"`
	LegacyWorktreeDir string            `json:"worktree_dir,omitempty"`
	// Set once dev created the namespace: dev rm must delete it even when no dev.yaml declares the app any more.
	HasTemporalNamespace bool                `json:"has_temporal_namespace,omitempty"`
	Apps                 map[string]AppState `json:"apps"`
}

// State files written before dev knew several repos hold platform's checkout as worktree_dir.
const legacyWorktreeRepo = "platform"

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
		return StackState{Name: stackName, CheckoutDirs: map[string]string{}, LegacyWorktreeDir: "", HasTemporalNamespace: false, Apps: map[string]AppState{}}, nil
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
	if stackState.LegacyWorktreeDir != "" {
		stackState.CheckoutDirs[legacyWorktreeRepo] = stackState.LegacyWorktreeDir
		stackState.LegacyWorktreeDir = ""
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
