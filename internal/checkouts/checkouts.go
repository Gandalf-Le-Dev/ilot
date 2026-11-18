package checkouts

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/stack"
)

type Checkout struct {
	Repo           string `json:"repo"`
	Dir            string `json:"dir"`
	Branch         string `json:"branch"`
	IsMainCheckout bool   `json:"is_main_checkout"`
}

const personalFileHint = "~/.config/ilot/env.yaml"

func ForStack(repoPaths map[string]string, stackName string) (map[string]Checkout, error) {
	checkoutByRepo := map[string]Checkout{}
	var problems []error
	for _, repoName := range slices.Sorted(maps.Keys(repoPaths)) {
		if _, err := os.Stat(repoPaths[repoName]); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		repoCheckouts, err := list(repoName, repoPaths[repoName])
		if err != nil {
			return nil, err
		}
		checkout, err := checkoutForStack(repoCheckouts, stackName)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		checkoutByRepo[repoName] = checkout
	}
	return checkoutByRepo, errors.Join(problems...)
}

func checkoutForStack(repoCheckouts []Checkout, stackName string) (Checkout, error) {
	mainCheckout := repoCheckouts[0]
	if stackName == stack.MasterStackName || stackName == stack.SharedStackName {
		return mainCheckout, nil
	}
	var matching []Checkout
	for _, linkedWorktree := range repoCheckouts[1:] {
		name, err := stack.NameForCheckout(linkedWorktree.Dir, linkedWorktree.Branch, false)
		if err == nil && name == stackName {
			matching = append(matching, linkedWorktree)
		}
	}
	switch len(matching) {
	case 0:
		return mainCheckout, nil
	case 1:
		return matching[0], nil
	}
	var matchingDirs []string
	for _, worktree := range matching {
		matchingDirs = append(matchingDirs, worktree.Dir)
	}
	return Checkout{}, SeveralWorktreesError{Repo: mainCheckout.Repo, StackName: stackName, WorktreeDirs: matchingDirs}
}

type SeveralWorktreesError struct {
	Repo         string
	StackName    string
	WorktreeDirs []string
}

func (err SeveralWorktreesError) Error() string {
	return fmt.Sprintf("repo %s has several worktrees for stack %s, keep one: %s", err.Repo, err.StackName, strings.Join(err.WorktreeDirs, ", "))
}

func list(repoName, repoPath string) ([]Checkout, error) {
	output, err := stack.GitOutput(repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list the worktrees of %s at %s: %w", repoName, repoPath, err)
	}
	var repoCheckouts []Checkout
	for _, entry := range strings.Split(strings.TrimSpace(output), "\n\n") {
		var checkout Checkout
		checkout.Repo = repoName
		for _, line := range strings.Split(entry, "\n") {
			if worktreeDir, isWorktreeLine := strings.CutPrefix(line, "worktree "); isWorktreeLine {
				checkout.Dir = worktreeDir
			}
			if branchRef, isBranchLine := strings.CutPrefix(line, "branch "); isBranchLine {
				checkout.Branch = strings.TrimPrefix(branchRef, "refs/heads/")
			}
		}
		checkout.IsMainCheckout = len(repoCheckouts) == 0
		if !checkout.IsMainCheckout && isDeleted(checkout.Dir) {
			continue
		}
		repoCheckouts = append(repoCheckouts, checkout)
	}
	if len(repoCheckouts) == 0 {
		return nil, fmt.Errorf("repo %s at %s has no checkout", repoName, repoPath)
	}
	return repoCheckouts, nil
}

// git keeps listing a worktree whose folder was deleted until git worktree prune runs.
func isDeleted(worktreeDir string) bool {
	_, err := os.Stat(worktreeDir)
	return errors.Is(err, fs.ErrNotExist)
}

func RepoOf(repoPaths map[string]string, dir string) (string, error) {
	commonGitDir, err := commonGitDirOf(dir)
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git checkout: %w", dir, err)
	}
	for _, repoName := range slices.Sorted(maps.Keys(repoPaths)) {
		repoCommonGitDir, err := commonGitDirOf(repoPaths[repoName])
		if err == nil && repoCommonGitDir == commonGitDir {
			return repoName, nil
		}
	}
	return "", fmt.Errorf("%s is in no repo that ilot knows: if it is one of %v cloned elsewhere, set repos.<name>.path in %s; for a personal repo, set repos.<name>.path and repos.<name>.config there", dir, slices.Sorted(maps.Keys(repoPaths)), personalFileHint)
}

func commonGitDirOf(dir string) (string, error) {
	output, err := stack.GitOutput(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(output)), nil
}

func IsSeveralWorktrees(err error) bool {
	var severalWorktrees SeveralWorktreesError
	return errors.As(err, &severalWorktrees)
}
