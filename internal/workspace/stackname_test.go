package workspace

import (
	"regexp"
	"strings"
	"testing"
)

const ticketPattern = `[A-Za-z]+-[0-9]+`

func TestDefaultStackName(t *testing.T) {
	testCases := []struct {
		name     string
		pattern  string
		worktree Worktree
		wantName string
	}{
		{name: "a main checkout is base on any branch", worktree: Worktree{Dir: "/code/shop", Branch: "retry-jobs", IsMain: true}, wantName: "base"},
		{name: "a main checkout is base with a pattern too", pattern: ticketPattern, worktree: Worktree{Dir: "/code/shop", Branch: "PROJ-12-x", IsMain: true}, wantName: "base"},

		{name: "the slug of the branch", worktree: Worktree{Dir: "/code/wt1", Branch: "retry-jobs"}, wantName: "retry-jobs"},
		{name: "a slash and capitals", worktree: Worktree{Dir: "/code/wt1", Branch: "feature/PROJ-1234_Retry.jobs"}, wantName: "feature-proj-1234-retry-jobs"},
		{name: "dashes at the ends", worktree: Worktree{Dir: "/code/wt1", Branch: "--weird--"}, wantName: "weird"},
		{name: "63 characters at most, with no dash at the end", worktree: Worktree{Dir: "/code/wt1", Branch: "a-name-that-goes-on-and-on-well-past-the-sixty-three-characters-of-a-dns-label"}, wantName: "a-name-that-goes-on-and-on-well-past-the-sixty-three-characters"},
		{name: "the folder does not count when a branch exists", worktree: Worktree{Dir: "/code/proj-9-folder", Branch: "retry-jobs"}, wantName: "retry-jobs"},

		{name: "the first match of the pattern", pattern: ticketPattern, worktree: Worktree{Dir: "/code/wt1", Branch: "feature/PROJ-1234-retry-jobs"}, wantName: "proj-1234"},
		{name: "the first of two matches", pattern: ticketPattern, worktree: Worktree{Dir: "/code/wt1", Branch: "OPS-7-after-TASK-9"}, wantName: "ops-7"},
		{name: "a branch that the pattern does not match", pattern: ticketPattern, worktree: Worktree{Dir: "/code/wt1", Branch: "hotfix"}, wantName: "hotfix"},
		{name: "the pattern does not read the folder", pattern: ticketPattern, worktree: Worktree{Dir: "/code/proj-9-folder", Branch: "hotfix"}, wantName: "hotfix"},
		{name: "a match is a slug too", pattern: `[a-z]+/[A-Z]+_[0-9]+`, worktree: Worktree{Dir: "/code/wt1", Branch: "dev/PROJ_12/x"}, wantName: "dev-proj-12"},

		{name: "a detached HEAD takes the folder", worktree: Worktree{Dir: "/code/Shop_Bisect.v2", Branch: ""}, wantName: "shop-bisect-v2"},
		{name: "a detached HEAD takes the whole folder with a pattern too", pattern: ticketPattern, worktree: Worktree{Dir: "/code/proj-12-bisect", Branch: ""}, wantName: "proj-12-bisect"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			workspace := Workspace{}
			if testCase.pattern != "" {
				workspace.StackPattern = regexp.MustCompile(testCase.pattern)
			}

			gotName, err := workspace.DefaultStackName(testCase.worktree)

			if err != nil || gotName != testCase.wantName {
				t.Errorf("DefaultStackName(%+v) = %q, %v; want %q", testCase.worktree, gotName, err, testCase.wantName)
			}
		})
	}
}

func TestDefaultStackNameRefusesANameThatIsEmptyOrReserved(t *testing.T) {
	testCases := []struct {
		worktree        Worktree
		wantErrorSubstr string
	}{
		{worktree: Worktree{Dir: "/code/wt1", Branch: "___"}, wantErrorSubstr: `the branch "___"`},
		{worktree: Worktree{Dir: "/code/___", Branch: ""}, wantErrorSubstr: `the folder "___"`},
		{worktree: Worktree{Dir: "/code/wt1", Branch: "Base"}, wantErrorSubstr: "the stack of the main checkouts"},
		{worktree: Worktree{Dir: "/code/base", Branch: ""}, wantErrorSubstr: "ilot stack bind"},
	}
	for _, testCase := range testCases {
		_, err := Workspace{}.DefaultStackName(testCase.worktree)
		if err == nil || !strings.Contains(err.Error(), testCase.wantErrorSubstr) {
			t.Errorf("DefaultStackName(%+v) = %v, want an error with %q", testCase.worktree, err, testCase.wantErrorSubstr)
		}
	}
}
