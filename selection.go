package main

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/state"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

// selection is the --workspace and --stack of a command that acts on one stack. The watcher of an app gives both.
type selection struct {
	Workspace string `help:"Workspace to act on, as its ilot.yaml names it. The default is the workspace of the current folder. With it, the command works from any folder."`
	Stack     string `help:"Stack to act on, as ilot ls names it. The default is the stack of the current folder, or base with --workspace."`
}

// workspaceSelection is the --workspace of a command that acts on every stack of a workspace.
type workspaceSelection struct {
	Workspace string `help:"Workspace to act on, as its ilot.yaml names it. The default is the workspace of the current folder. With it, the command works from any folder."`
}

func (chosen workspaceSelection) selection() selection {
	return selection{Workspace: chosen.Workspace, Stack: ""}
}

// selectedWorkspace gives the workspace of --workspace, or the one of the current folder.
// With --workspace, the worktree is the main checkout of the root repo, which the state records at the first start in the workspace.
func selectedWorkspace(workspaceName string) (workspace.Workspace, workspace.Worktree, error) {
	if workspaceName == "" {
		return findCurrentWorkspace()
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return workspace.Workspace{}, workspace.Worktree{}, err
	}
	current, err := state.Store{RootDir: stateDirOf(homeDir)}.Load()
	if err != nil {
		return workspace.Workspace{}, workspace.Worktree{}, err
	}
	known, isKnown := current.Workspaces[workspaceName]
	if !isKnown || known.Root == "" {
		return workspace.Workspace{}, workspace.Worktree{}, fmt.Errorf("ilot knows no workspace %q: it learns one at the first ilot start in a repo of it. Known workspaces: %s", workspaceName, knownWorkspaces(current))
	}
	found, rootCheckout, err := findWorkspaceOf(known.Root)
	if err != nil {
		return workspace.Workspace{}, workspace.Worktree{}, fmt.Errorf("read the workspace %s at %s: %w", workspaceName, known.Root, err)
	}
	if found.Name != workspaceName {
		return workspace.Workspace{}, workspace.Worktree{}, fmt.Errorf("the repo %s, which the state records as the root of the workspace %s, now gives the workspace %s", known.Root, workspaceName, found.Name)
	}
	return found, rootCheckout, nil
}

func knownWorkspaces(current state.State) string {
	var names []string
	for _, name := range slices.Sorted(maps.Keys(current.Workspaces)) {
		if current.Workspaces[name].Root != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// stackOf gives the stack of the command. A stack that --stack names must be in the state, so that a typo does not make a new stack.
// Only the stack of the current folder can get a binding.
func (chosen selection) stackOf(ctx context.Context, store state.Store, currentWorkspace workspace.Workspace, worktree workspace.Worktree, recordsBinding bool) (workspace.Workspace, string, error) {
	switch {
	case chosen.Stack != "":
		if chosen.Stack == workspace.BaseStackName {
			return currentWorkspace, chosen.Stack, nil
		}
		current, err := store.Load()
		if err != nil {
			return workspace.Workspace{}, "", err
		}
		if _, isKnown := current.Stacks(currentWorkspace.Name)[chosen.Stack]; !isKnown {
			return workspace.Workspace{}, "", fmt.Errorf("the workspace %s has no stack %q: ilot ls lists its stacks", currentWorkspace.Name, chosen.Stack)
		}
		return currentWorkspace, chosen.Stack, nil
	case chosen.Workspace != "":
		return currentWorkspace, workspace.BaseStackName, nil
	}
	return stackOfWorktree(ctx, store, currentWorkspace, worktree, recordsBinding, os.Stderr)
}
