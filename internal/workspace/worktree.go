package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Worktree is one checkout of a repo: its main checkout, or a worktree that git worktree add made.
type Worktree struct {
	// Empty until the workspace finds the repo of the worktree.
	Repo string `json:"repo"`
	Dir  string `json:"dir"`
	// Empty with a detached HEAD.
	Branch string `json:"branch"`
	IsMain bool   `json:"is_main_checkout"`
	// The main checkout of the repo.
	MainDir string `json:"-"`
}

// Locate finds the worktree that holds a folder, with one git process.
// git worktree list gives the branch on an unborn HEAD too, where git rev-parse --abbrev-ref HEAD fails.
func (git *Git) Locate(dirInsideWorktree string) (Worktree, error) {
	dir, err := Canonical(dirInsideWorktree)
	if err != nil {
		return Worktree{}, err
	}
	worktrees, err := git.list("", dir)
	if err != nil {
		return Worktree{}, fmt.Errorf("%s is not inside a git checkout: %w", dirInsideWorktree, err)
	}
	located := -1
	for index, worktree := range worktrees {
		isInside := dir == worktree.Dir || strings.HasPrefix(dir, worktree.Dir+string(filepath.Separator))
		// A worktree can sit inside the folder of another one, so the longest path wins.
		if isInside && (located < 0 || len(worktree.Dir) > len(worktrees[located].Dir)) {
			located = index
		}
	}
	if located < 0 {
		return Worktree{}, fmt.Errorf("%s is in no worktree that git lists for its repo", dirInsideWorktree)
	}
	return worktrees[located], nil
}

// Worktrees lists the worktrees of a repo with one git process. The main checkout is the first one.
func (git *Git) Worktrees(repoName, repoDir string) ([]Worktree, error) {
	listed, err := git.list(repoName, repoDir)
	if err != nil {
		return nil, fmt.Errorf("list the worktrees of %s at %s: %w", repoName, repoDir, err)
	}
	var worktrees []Worktree
	for _, worktree := range listed {
		if !worktree.IsMain && isDeleted(worktree.Dir) {
			continue
		}
		worktrees = append(worktrees, worktree)
	}
	return worktrees, nil
}

func (git *Git) list(repoName, dir string) ([]Worktree, error) {
	output, err := git.Output(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var worktrees []Worktree
	for entry := range strings.SplitSeq(strings.TrimSpace(output), "\n\n") {
		worktree := Worktree{Repo: repoName, Dir: "", Branch: "", IsMain: len(worktrees) == 0, MainDir: ""}
		for line := range strings.SplitSeq(entry, "\n") {
			if worktreeDir, isWorktreeLine := strings.CutPrefix(line, "worktree "); isWorktreeLine {
				// git lists the path that it recorded at git worktree add. A link can be in that path today.
				if worktree.Dir, err = Canonical(worktreeDir); err != nil {
					return nil, err
				}
			}
			if branchRef, isBranchLine := strings.CutPrefix(line, "branch "); isBranchLine {
				worktree.Branch = strings.TrimPrefix(branchRef, "refs/heads/")
			}
		}
		if worktree.Dir == "" {
			continue
		}
		worktrees = append(worktrees, worktree)
	}
	if len(worktrees) == 0 {
		return nil, fmt.Errorf("git lists no checkout at %s", dir)
	}
	for index := range worktrees {
		worktrees[index].MainDir = worktrees[0].Dir
	}
	return worktrees, nil
}

// git keeps listing a worktree whose folder was deleted until git worktree prune runs.
func isDeleted(worktreeDir string) bool {
	_, err := os.Stat(worktreeDir)
	return errors.Is(err, fs.ErrNotExist)
}
