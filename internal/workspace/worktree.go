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

const detachedHead = "HEAD"

// Locate finds the worktree that holds a folder, with one git process.
func (git *Git) Locate(dirInsideWorktree string) (Worktree, error) {
	output, err := git.Output(dirInsideWorktree, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir", "--abbrev-ref", "HEAD")
	if err != nil {
		return Worktree{}, fmt.Errorf("%s is not inside a git checkout: %w", dirInsideWorktree, err)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 4 {
		return Worktree{}, fmt.Errorf("unexpected git rev-parse output %q", output)
	}
	// git resolves the links of these three paths itself, so they compare equal to a canonical path.
	worktreeDir, gitDir, commonGitDir, branch := lines[0], lines[1], lines[2], lines[3]
	if branch == detachedHead {
		branch = ""
	}
	return Worktree{Repo: "", Dir: worktreeDir, Branch: branch, IsMain: gitDir == commonGitDir, MainDir: filepath.Dir(commonGitDir)}, nil
}

// Worktrees lists the worktrees of a repo with one git process. The main checkout is the first one.
func (git *Git) Worktrees(repoName, repoDir string) ([]Worktree, error) {
	output, err := git.Output(repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list the worktrees of %s at %s: %w", repoName, repoDir, err)
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
		if worktree.Dir == "" || (!worktree.IsMain && isDeleted(worktree.Dir)) {
			continue
		}
		worktrees = append(worktrees, worktree)
	}
	if len(worktrees) == 0 {
		return nil, fmt.Errorf("repo %s at %s has no checkout", repoName, repoDir)
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
