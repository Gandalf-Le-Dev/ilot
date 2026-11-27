package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocateFindsTheMainCheckoutFromALinkedWorktree(t *testing.T) {
	mainCheckoutDir := newRepo(t, "shop")
	linkedWorktreeDir := addWorktree(t, mainCheckoutDir, "retry", "feature/retry-jobs")
	git := NewGit()

	fromLinkedWorktree, err := git.Locate(linkedWorktreeDir)
	if want := (Worktree{Repo: "", Dir: linkedWorktreeDir, Branch: "feature/retry-jobs", IsMain: false, MainDir: mainCheckoutDir}); err != nil || fromLinkedWorktree != want {
		t.Errorf("Locate(linked) = %+v, %v; want %+v", fromLinkedWorktree, err, want)
	}

	fromMainCheckout, err := git.Locate(mainCheckoutDir)
	if want := (Worktree{Repo: "", Dir: mainCheckoutDir, Branch: "main", IsMain: true, MainDir: mainCheckoutDir}); err != nil || fromMainCheckout != want {
		t.Errorf("Locate(main) = %+v, %v; want %+v", fromMainCheckout, err, want)
	}
}

func TestLocateFromASubfolderGivesTheTopOfTheWorktree(t *testing.T) {
	mainCheckoutDir := newRepoAt(t, filepath.Join(canonicalTempDir(t), "shop"), map[string]string{"cmd/api/main.go": "package main\n"})

	located, err := NewGit().Locate(filepath.Join(mainCheckoutDir, "cmd", "api"))

	if err != nil || located.Dir != mainCheckoutDir {
		t.Errorf("Locate(subfolder) = %+v, %v; want the top folder %s", located, err, mainCheckoutDir)
	}
}

func TestAWorktreeWithADetachedHeadHasNoBranch(t *testing.T) {
	mainCheckoutDir := newRepo(t, "shop")
	linkedWorktreeDir := addWorktree(t, mainCheckoutDir, "bisect", "feature/bisect")
	gitInTest(t, linkedWorktreeDir, "checkout", "-q", "--detach")
	git := NewGit()

	located, err := git.Locate(linkedWorktreeDir)
	if err != nil || located.Branch != "" {
		t.Errorf("Locate = %+v, %v; want no branch", located, err)
	}
	worktrees, err := git.Worktrees("shop", mainCheckoutDir)
	if err != nil || len(worktrees) != 2 || worktrees[1].Branch != "" {
		t.Errorf("Worktrees = %+v, %v; want no branch for the second one", worktrees, err)
	}
}

func TestWorktreesListsTheMainCheckoutFirstAndSkipsADeletedFolder(t *testing.T) {
	mainCheckoutDir := newRepo(t, "shop")
	deletedWorktreeDir := addWorktree(t, mainCheckoutDir, "old-attempt", "old")
	if err := os.RemoveAll(deletedWorktreeDir); err != nil {
		t.Fatal(err)
	}
	keptWorktreeDir := addWorktree(t, mainCheckoutDir, "new-attempt", "new")

	worktrees, err := NewGit().Worktrees("shop", keptWorktreeDir)

	want := []Worktree{
		{Repo: "shop", Dir: mainCheckoutDir, Branch: "main", IsMain: true, MainDir: mainCheckoutDir},
		{Repo: "shop", Dir: keptWorktreeDir, Branch: "new", IsMain: false, MainDir: mainCheckoutDir},
	}
	if err != nil || len(worktrees) != 2 || worktrees[0] != want[0] || worktrees[1] != want[1] {
		t.Errorf("Worktrees = %+v, %v; want %+v", worktrees, err, want)
	}
}

// git records the path of a repo and of a worktree on the day of git worktree add. After the parent folder moved
// behind a link, these recorded paths are the paths through the link, and the paths that git reads from the system are not.
func TestARepoWhoseFolderMovedBehindALinkIsStillOneRepo(t *testing.T) {
	parentDir := canonicalTempDir(t)
	oldDir, newDir := filepath.Join(parentDir, "code"), filepath.Join(parentDir, "disk2")
	mainCheckoutDir := newRepoAt(t, filepath.Join(oldDir, "shop"), nil)
	gitInTest(t, mainCheckoutDir, "worktree", "add", "-q", "-b", "retry", filepath.Join(oldDir, "shop-retry"))
	if err := os.Rename(oldDir, newDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newDir, oldDir); err != nil {
		t.Fatal(err)
	}
	git := NewGit()
	movedMainCheckoutDir, movedWorktreeDir := filepath.Join(newDir, "shop"), filepath.Join(newDir, "shop-retry")

	located, err := git.Locate(filepath.Join(oldDir, "shop-retry"))
	if err != nil || located.Dir != movedWorktreeDir || located.MainDir != movedMainCheckoutDir {
		t.Errorf("Locate = %+v, %v; want the worktree %s of the repo %s", located, err, movedWorktreeDir, movedMainCheckoutDir)
	}
	worktrees, err := git.Worktrees("shop", filepath.Join(oldDir, "shop"))
	if err != nil || len(worktrees) != 2 || worktrees[0].Dir != movedMainCheckoutDir || worktrees[1].Dir != movedWorktreeDir {
		t.Errorf("Worktrees = %+v, %v; want %s then %s", worktrees, err, movedMainCheckoutDir, movedWorktreeDir)
	}
}
