package stack

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNameForCheckout(t *testing.T) {
	testCases := []struct {
		worktreeDir    string
		isMainCheckout bool
		wantName       string
	}{
		{worktreeDir: "/home/user/go/src/platform", isMainCheckout: true, wantName: "master"},
		{worktreeDir: "/home/user/worktrees/platform/proj-2002", wantName: "proj-2002"},
		{worktreeDir: "/home/user/worktrees-Dev_CLI.v2", wantName: "worktrees-dev-cli-v2"},
		{worktreeDir: "/home/user/--weird--", wantName: "weird"},
		{worktreeDir: "/home/user/a-name-that-goes-on-and-on-well-past-the-sixty-three-characters-of-a-dns-label", wantName: "a-name-that-goes-on-and-on-well-past-the-sixty-three-characters"},
	}
	for _, testCase := range testCases {
		gotName, err := NameForCheckout(testCase.worktreeDir, "", testCase.isMainCheckout)
		if err != nil {
			t.Errorf("NameForCheckout(%q): %v", testCase.worktreeDir, err)
			continue
		}
		if gotName != testCase.wantName {
			t.Errorf("NameForCheckout(%q) = %q, want %q", testCase.worktreeDir, gotName, testCase.wantName)
		}
	}
}

func TestNameForCheckoutRefusesANameWithNoUsableCharacter(t *testing.T) {
	if _, err := NameForCheckout("/home/user/___", "", false); err == nil {
		t.Fatal("want an error for a directory name with no letter or digit")
	}
}

func TestNameForCheckoutRefusesTheReservedStackNames(t *testing.T) {
	for _, reservedName := range []string{"master", "devbox"} {
		if _, err := NameForCheckout("/home/user/worktrees/"+reservedName, "", false); err == nil {
			t.Errorf("NameForCheckout(%q) gave no error: the worktree would share a reserved stack", reservedName)
		}
	}
}

func TestNameForCheckoutUsesTheTicketKey(t *testing.T) {
	testCases := []struct {
		checkoutDir    string
		branch         string
		isMainCheckout bool
		wantName       string
	}{
		{checkoutDir: "/w/ilot-jdoe-proj-2003-example-change", branch: "dev/jdoe/proj-2003-example-change", wantName: "proj-2003"},
		{checkoutDir: "/w/task-2004", branch: "TASK-2004", wantName: "task-2004"},
		{checkoutDir: "/w/anything", branch: "OPS-123-fix", wantName: "ops-123"},
		{checkoutDir: "/w/anything", branch: "PROJ-2005-x", wantName: "proj-2005"},
		{checkoutDir: "/w/hotfix", branch: "HOTFIX", wantName: "hotfix"},
		{checkoutDir: "/w/proj-2005-example-feature", branch: "feature/example", wantName: "proj-2005"},
		{checkoutDir: "/w/schemas", branch: "dev/jdoe/some-change", isMainCheckout: true, wantName: "master"},
		{checkoutDir: "/w/reproj-12", branch: "sproj-12", wantName: "reproj-12"},
	}
	for _, testCase := range testCases {
		gotName, err := NameForCheckout(testCase.checkoutDir, testCase.branch, testCase.isMainCheckout)
		if err != nil {
			t.Errorf("NameForCheckout(%q, %q): %v", testCase.checkoutDir, testCase.branch, err)
			continue
		}
		if gotName != testCase.wantName {
			t.Errorf("NameForCheckout(%q, %q) = %q, want %q", testCase.checkoutDir, testCase.branch, gotName, testCase.wantName)
		}
	}
}

func TestLocateReadsTheBranch(t *testing.T) {
	mainCheckoutDir := filepath.Join(t.TempDir(), "platform")
	linkedWorktreeDir := filepath.Join(t.TempDir(), "example-feature")
	runGit(t, filepath.Dir(mainCheckoutDir), "init", "-q", mainCheckoutDir)
	runGit(t, mainCheckoutDir, "commit", "-q", "--allow-empty", "-m", "initial")
	runGit(t, mainCheckoutDir, "worktree", "add", "-q", "-b", "PROJ-1234-x", linkedWorktreeDir)

	located, err := Locate(linkedWorktreeDir)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if located.Name != "proj-1234" || located.Branch != "PROJ-1234-x" {
		t.Errorf("Locate = %+v", located)
	}
}

func runGit(t *testing.T, dir string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, arguments...)...)
	environmentWithoutGit := slices.DeleteFunc(command.Environ(), func(variable string) bool { return strings.HasPrefix(variable, "GIT_") })
	command.Env = append(environmentWithoutGit, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.net", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.net")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func TestLocateIgnoresTheGitEnvironmentOfAnEnclosingHook(t *testing.T) {
	decoyRepoDir := filepath.Join(t.TempDir(), "decoy")
	runGit(t, filepath.Dir(decoyRepoDir), "init", "-q", decoyRepoDir)
	t.Setenv("GIT_DIR", filepath.Join(decoyRepoDir, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoyRepoDir, ".git", "index"))

	TestLocateFindsTheMainCheckoutFromALinkedWorktree(t)

	decoyConfig, err := os.ReadFile(filepath.Join(decoyRepoDir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(decoyConfig), "worktree") {
		t.Errorf("the git calls wrote into the repo of GIT_DIR:\n%s", decoyConfig)
	}
}

func TestLocateFindsTheMainCheckoutFromALinkedWorktree(t *testing.T) {
	mainCheckoutDir := filepath.Join(t.TempDir(), "platform")
	linkedWorktreeDir := filepath.Join(t.TempDir(), "proj-1234")
	runGit(t, filepath.Dir(mainCheckoutDir), "init", "-q", mainCheckoutDir)
	runGit(t, mainCheckoutDir, "commit", "-q", "--allow-empty", "-m", "initial")
	runGit(t, mainCheckoutDir, "worktree", "add", "-q", linkedWorktreeDir)

	fromLinkedWorktree, err := Locate(filepath.Join(linkedWorktreeDir))
	if err != nil {
		t.Fatalf("Locate(linked): %v", err)
	}
	if fromLinkedWorktree.Name != "proj-1234" || fromLinkedWorktree.WorktreeDir != linkedWorktreeDir || fromLinkedWorktree.MainCheckoutDir != mainCheckoutDir {
		t.Errorf("Locate(linked) = %+v", fromLinkedWorktree)
	}

	fromMainCheckout, err := Locate(mainCheckoutDir)
	if err != nil {
		t.Fatalf("Locate(main): %v", err)
	}
	if fromMainCheckout.Name != MasterStackName || fromMainCheckout.WorktreeDir != mainCheckoutDir || fromMainCheckout.MainCheckoutDir != mainCheckoutDir {
		t.Errorf("Locate(main) = %+v", fromMainCheckout)
	}
}
