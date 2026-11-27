package workspace

import (
	"fmt"
	"maps"
	"slices"
)

// Bindings gives the worktrees of each stack: from the name of a stack to its worktree of each repo.
// A worktree gets its stack one time, so a git switch, a rebase or a bisect in it does not move it to another stack.
// The base stack has no binding: it is the main checkout of every repo.
type Bindings map[string]map[string]string

// StackOf gives the stack that a worktree is bound to.
func (bindings Bindings) StackOf(worktree Worktree) (stackName string, isBound bool) {
	for _, candidate := range slices.Sorted(maps.Keys(bindings)) {
		if bindings[candidate][worktree.Repo] == worktree.Dir {
			return candidate, true
		}
	}
	return "", false
}

// Bind gives a copy of the bindings where the worktree belongs to the stack, and the stack that it leaves.
// A stack takes one worktree of a repo.
func (bindings Bindings) Bind(stackName string, worktree Worktree) (bound Bindings, previousStackName string, err error) {
	switch {
	case worktree.IsMain:
		return nil, "", fmt.Errorf("%s is the main checkout of the repo %s, which is always the %s stack: only a linked worktree has a binding", worktree.Dir, worktree.Repo, BaseStackName)
	case stackName == BaseStackName:
		return nil, "", fmt.Errorf("the %s stack is the main checkout of every repo: bind the worktree %s to another name", BaseStackName, worktree.Dir)
	case stackName == "" || slug(stackName) != stackName:
		return nil, "", fmt.Errorf("the stack name %q goes in the URL of every app, so it takes lowercase letters, digits and dashes only, %d characters at most", stackName, dnsLabelMaxLength)
	}
	if boundDir := bindings[stackName][worktree.Repo]; boundDir != "" && boundDir != worktree.Dir && !isDeleted(boundDir) {
		return nil, "", fmt.Errorf("the stack %s already has the worktree %s of the repo %s, and a stack takes one worktree of a repo: give %s another stack with ilot stack bind <name>", stackName, boundDir, worktree.Repo, worktree.Dir)
	}
	bound = Bindings{}
	for otherStackName, dirByRepo := range bindings {
		keptDirByRepo := maps.Clone(dirByRepo)
		if keptDirByRepo[worktree.Repo] == worktree.Dir {
			delete(keptDirByRepo, worktree.Repo)
			if otherStackName != stackName {
				previousStackName = otherStackName
			}
		}
		if len(keptDirByRepo) > 0 {
			bound[otherStackName] = keptDirByRepo
		}
	}
	if bound[stackName] == nil {
		bound[stackName] = map[string]string{}
	}
	bound[stackName][worktree.Repo] = worktree.Dir
	return bound, previousStackName, nil
}

// StackOf gives the stack of a worktree: base for a main checkout, the stack of its binding,
// and else the name that the first ilot start binds it to.
func (workspace Workspace) StackOf(worktree Worktree) (stackName string, isBound bool, err error) {
	if worktree.IsMain {
		return BaseStackName, true, nil
	}
	if stackName, isBound = workspace.Bindings.StackOf(worktree); isBound {
		return stackName, true, nil
	}
	stackName, err = workspace.DefaultStackName(worktree)
	return stackName, false, err
}
