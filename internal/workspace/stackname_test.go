package workspace

import "testing"

func TestStackNameOfAFolder(t *testing.T) {
	testCases := []struct {
		worktreeDir string
		isMain      bool
		wantName    string
	}{
		{worktreeDir: "/home/user/go/src/platform", isMain: true, wantName: "master"},
		{worktreeDir: "/home/user/worktrees/platform/proj-2002", wantName: "proj-2002"},
		{worktreeDir: "/home/user/worktrees-Dev_CLI.v2", wantName: "worktrees-dev-cli-v2"},
		{worktreeDir: "/home/user/--weird--", wantName: "weird"},
		{worktreeDir: "/home/user/a-name-that-goes-on-and-on-well-past-the-sixty-three-characters-of-a-dns-label", wantName: "a-name-that-goes-on-and-on-well-past-the-sixty-three-characters"},
	}
	for _, testCase := range testCases {
		gotName, err := StackName(Worktree{Dir: testCase.worktreeDir, IsMain: testCase.isMain})
		if err != nil || gotName != testCase.wantName {
			t.Errorf("StackName(%q) = %q, %v; want %q", testCase.worktreeDir, gotName, err, testCase.wantName)
		}
	}
}

func TestStackNameRefusesANameWithNoUsableCharacter(t *testing.T) {
	if _, err := StackName(Worktree{Dir: "/home/user/___"}); err == nil {
		t.Fatal("want an error for a directory name with no letter or digit")
	}
}

func TestStackNameRefusesTheReservedStackNames(t *testing.T) {
	for _, reservedName := range []string{"master", "devbox"} {
		if _, err := StackName(Worktree{Dir: "/home/user/worktrees/" + reservedName}); err == nil {
			t.Errorf("StackName(%q) gave no error: the worktree would share a reserved stack", reservedName)
		}
	}
}

func TestStackNameUsesTheTicketKey(t *testing.T) {
	testCases := []struct {
		worktreeDir string
		branch      string
		isMain      bool
		wantName    string
	}{
		{worktreeDir: "/w/ilot-jdoe-proj-2003-example-change", branch: "dev/jdoe/proj-2003-example-change", wantName: "proj-2003"},
		{worktreeDir: "/w/task-2004", branch: "TASK-2004", wantName: "task-2004"},
		{worktreeDir: "/w/anything", branch: "OPS-123-fix", wantName: "ops-123"},
		{worktreeDir: "/w/hotfix", branch: "HOTFIX", wantName: "hotfix"},
		{worktreeDir: "/w/proj-2005-example-feature", branch: "feature/example", wantName: "proj-2005"},
		{worktreeDir: "/w/schemas", branch: "dev/jdoe/some-change", isMain: true, wantName: "master"},
		{worktreeDir: "/w/reproj-12", branch: "sproj-12", wantName: "reproj-12"},
	}
	for _, testCase := range testCases {
		gotName, err := StackName(Worktree{Dir: testCase.worktreeDir, Branch: testCase.branch, IsMain: testCase.isMain})
		if err != nil || gotName != testCase.wantName {
			t.Errorf("StackName(%q, %q) = %q, %v; want %q", testCase.worktreeDir, testCase.branch, gotName, err, testCase.wantName)
		}
	}
}
