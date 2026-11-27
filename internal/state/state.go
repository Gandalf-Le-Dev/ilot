// Package state holds what ilot remembers between two commands: the stacks of every workspace, their apps and their ports.
package state

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	// The version of the file. A file of another version is refused, never guessed.
	CurrentVersion = 1

	defaultLockTimeout = 10 * time.Second
	lockPollInterval   = 50 * time.Millisecond
)

// State is the content of the state file: one file for every workspace, so that the port table changes in one write.
type State struct {
	Version    int                   `json:"version"`
	Workspaces map[string]*Workspace `json:"workspaces"`
}

type Workspace struct {
	// The main checkout of the root repo. Two workspaces of one name have two roots.
	Root   string            `json:"root"`
	Stacks map[string]*Stack `json:"stacks"`
}

type Stack struct {
	// For the base stack, the main checkout of each repo. For another stack, the binding: its worktree of each repo that has one.
	Checkouts map[string]string `json:"checkouts"`
	Apps      map[string]App    `json:"apps"`
}

type App struct {
	// The membership: the stack owns the app, and the app must run. The real status comes from the daemon.
	Wanted bool           `json:"wanted"`
	Ports  map[string]int `json:"ports"`
	// Stored so that the route table, ilot ls, ilot stop and ilot rm need no ilot.yaml.
	Route *Route `json:"route,omitempty"`
	// The name of the container of a container app, so that a stop can remove it with no ilot.yaml.
	Container string `json:"container,omitempty"`
}

type Route struct {
	Port     int    `json:"port"`
	HomePath string `json:"home_path,omitempty"`
	// The folder where ilot start starts the app.
	Dir string `json:"dir,omitempty"`
}

func New() State {
	return State{Version: CurrentVersion, Workspaces: map[string]*Workspace{}}
}

// Claim records the root of a workspace, and refuses a second workspace of the same name: their stacks would be the same stacks.
func (state *State) Claim(workspaceName, rootDir string) error {
	if err := state.CheckClaim(workspaceName, rootDir); err != nil {
		return err
	}
	state.workspace(workspaceName).Root = rootDir
	return nil
}

// CheckClaim is Claim for a command that writes nothing.
func (state State) CheckClaim(workspaceName, rootDir string) error {
	known, isKnown := state.Workspaces[workspaceName]
	if !isKnown || known.Root == "" || known.Root == rootDir {
		return nil
	}
	// A workspace whose folder is gone gives its name back.
	if _, err := os.Stat(known.Root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("two workspaces have the name %q: %s and %s. Set name in the ilot.yaml of one of them", workspaceName, known.Root, rootDir)
}

func (state *State) workspace(workspaceName string) *Workspace {
	if state.Workspaces == nil {
		state.Workspaces = map[string]*Workspace{}
	}
	known, isKnown := state.Workspaces[workspaceName]
	if !isKnown {
		known = &Workspace{Root: "", Stacks: map[string]*Stack{}}
		state.Workspaces[workspaceName] = known
	}
	if known.Stacks == nil {
		known.Stacks = map[string]*Stack{}
	}
	return known
}

// Stack gives the stack to change, and makes it when the state does not have it.
func (state *State) Stack(workspaceName, stackName string) *Stack {
	known := state.workspace(workspaceName)
	stack, isKnown := known.Stacks[stackName]
	if !isKnown {
		stack = &Stack{Checkouts: map[string]string{}, Apps: map[string]App{}}
		known.Stacks[stackName] = stack
	}
	if stack.Checkouts == nil {
		stack.Checkouts = map[string]string{}
	}
	if stack.Apps == nil {
		stack.Apps = map[string]App{}
	}
	return stack
}

// Stacks gives the stacks of a workspace by name. The map is nil for a workspace that the state does not have.
func (state State) Stacks(workspaceName string) map[string]*Stack {
	known, isKnown := state.Workspaces[workspaceName]
	if !isKnown {
		return nil
	}
	return known.Stacks
}

// App gives an app of a stack. An app that the state does not have is not wanted and has no port.
func (state State) App(workspaceName, stackName, appName string) App {
	stack, isKnown := state.Stacks(workspaceName)[stackName]
	if !isKnown {
		return App{Wanted: false, Ports: nil, Route: nil, Container: ""}
	}
	return stack.Apps[appName]
}

// HasApp tells if a stack of a workspace has an app, wanted or not.
func (state State) HasApp(workspaceName, stackName, appName string) bool {
	stack, isKnown := state.Stacks(workspaceName)[stackName]
	if !isKnown {
		return false
	}
	_, hasApp := stack.Apps[appName]
	return hasApp
}

func (state *State) RemoveStack(workspaceName, stackName string) {
	delete(state.Stacks(workspaceName), stackName)
}

// PortsInUse gives the ports of every app of every workspace: a stopped app keeps its ports.
func (state State) PortsInUse() map[int]bool {
	portsInUse := map[int]bool{}
	for _, known := range state.Workspaces {
		for _, stack := range known.Stacks {
			for _, app := range stack.Apps {
				for _, port := range app.Ports {
					portsInUse[port] = true
				}
			}
		}
	}
	return portsInUse
}

func (state State) Clone() State {
	cloned := State{Version: state.Version, Workspaces: map[string]*Workspace{}}
	for workspaceName, known := range state.Workspaces {
		clonedWorkspace := &Workspace{Root: known.Root, Stacks: map[string]*Stack{}}
		for stackName, stack := range known.Stacks {
			clonedStack := &Stack{Checkouts: maps.Clone(stack.Checkouts), Apps: map[string]App{}}
			for appName, app := range stack.Apps {
				clonedApp := App{Wanted: app.Wanted, Ports: maps.Clone(app.Ports), Route: nil, Container: app.Container}
				if app.Route != nil {
					clonedApp.Route = new(*app.Route)
				}
				clonedStack.Apps[appName] = clonedApp
			}
			clonedWorkspace.Stacks[stackName] = clonedStack
		}
		cloned.Workspaces[workspaceName] = clonedWorkspace
	}
	return cloned
}

type Store struct {
	RootDir string
	// How long Lock waits for another ilot. Zero gives 10 seconds.
	LockTimeout time.Duration
	// Called one time when Lock starts to wait, with what the holder wrote about itself.
	OnLockWait func(holder string)
}

func (store Store) File() string {
	return filepath.Join(store.RootDir, "state.json")
}

// BackupFile holds the version before the last Save.
func (store Store) BackupFile() string {
	return store.File() + ".bak"
}

func (store Store) lockFile() string {
	return filepath.Join(store.RootDir, "state.lock")
}

// StackDir holds the logs and the binaries of a stack.
func (store Store) StackDir(workspaceName, stackName string) string {
	return filepath.Join(store.RootDir, "workspaces", workspaceName, stackName)
}

// Load gives an empty state when no file exists.
func (store Store) Load() (State, error) {
	content, err := os.ReadFile(store.File())
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(content, &state); err != nil {
		return State{}, fmt.Errorf("read %s: %w: fix the file, or put %s in its place, which is the version before the last change", store.File(), err, store.BackupFile())
	}
	if state.Version != CurrentVersion {
		return State{}, fmt.Errorf("%s has the version %d, and this ilot reads the version %d: use the ilot that wrote it, or stop its apps and delete the file", store.File(), state.Version, CurrentVersion)
	}
	if state.Workspaces == nil {
		state.Workspaces = map[string]*Workspace{}
	}
	return state, nil
}

// Save replaces the file in one rename, after the content is on the disk, and keeps the last version as the backup.
func (store Store) Save(state State) error {
	if err := os.MkdirAll(store.RootDir, 0o700); err != nil {
		return err
	}
	state.Version = CurrentVersion
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporaryFile := store.File() + ".tmp"
	if err := writeSynced(temporaryFile, append(content, '\n')); err != nil {
		return err
	}
	if err := store.keepBackup(); err != nil {
		return err
	}
	return os.Rename(temporaryFile, store.File())
}

func writeSynced(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	return file.Close()
}

// A hard link, so that the state file is never absent between two renames.
func (store Store) keepBackup() error {
	if _, err := os.Stat(store.File()); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := os.Remove(store.BackupFile()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Link(store.File(), store.BackupFile())
}

// Lock lets one ilot at a time read and change the state. It gives up after the timeout, with what the holder wrote about itself.
func (store Store) Lock(ctx context.Context) (release func() error, err error) {
	if err := os.MkdirAll(store.RootDir, 0o700); err != nil {
		return nil, err
	}
	lockFile, err := os.OpenFile(store.lockFile(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	timeout := cmp.Or(store.LockTimeout, defaultLockTimeout)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	hasSaidThatItWaits := false
	for {
		err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			writeHolder(lockFile)
			return lockFile.Close, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.Join(fmt.Errorf("lock %s: %w", lockFile.Name(), err), lockFile.Close())
		}
		if !hasSaidThatItWaits && store.OnLockWait != nil {
			store.OnLockWait(store.lockHolder())
		}
		hasSaidThatItWaits = true
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), lockFile.Close())
		case <-deadline.C:
			return nil, errors.Join(fmt.Errorf("another ilot holds the state lock %s for more than %s: %s. Run the command again when it ends", lockFile.Name(), timeout, store.lockHolder()), lockFile.Close())
		case <-time.After(lockPollInterval):
		}
	}
}

// The lock is the proof. The text is for the ilot that waits, so a failed write changes nothing.
func writeHolder(lockFile *os.File) {
	if err := lockFile.Truncate(0); err != nil {
		return
	}
	_, _ = lockFile.WriteAt(fmt.Appendf(nil, "process %d (%s)\n", os.Getpid(), strings.Join(os.Args, " ")), 0)
}

func (store Store) lockHolder() string {
	content, err := os.ReadFile(store.lockFile())
	if holder := strings.TrimSpace(string(content)); err == nil && holder != "" {
		return holder
	}
	return "its holder wrote no name"
}
