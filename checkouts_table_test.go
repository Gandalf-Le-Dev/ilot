package main

import (
	"bytes"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

func TestCheckoutsTableShowsOnlyTheWorktreesOfTicketStacks(t *testing.T) {
	mainCheckouts := map[string]workspace.Worktree{
		"portal":   {Repo: "portal", Dir: "/home/user/acme-portal", Branch: "main", IsMain: true},
		"platform": {Repo: "platform", Dir: "/home/user/go/src/platform", Branch: "main", IsMain: true},
	}
	checkoutsByStack := map[string]map[string]workspace.Worktree{
		"base": mainCheckouts,
		"proj-0001": {
			"portal":   {Repo: "portal", Dir: "/home/user/worktrees-portal-0001", Branch: "PROJ-0001-x", IsMain: false},
			"platform": mainCheckouts["platform"],
		},
		"task-7": mainCheckouts,
	}
	var output bytes.Buffer

	if err := printWorktreeCheckouts(&output, checkoutsByStack, "/home/user"); err != nil {
		t.Fatal(err)
	}

	want := "\nCHECKOUT   REPO    FOLDER                   BRANCH\n" +
		"proj-0001  portal  ~/worktrees-portal-0001  PROJ-0001-x\n"
	if output.String() != want {
		t.Errorf("table =\n%s\nwant\n%s", output.String(), want)
	}
}

func TestCheckoutsTableIsEmptyWithoutWorktrees(t *testing.T) {
	mainCheckout := map[string]workspace.Worktree{"platform": {Repo: "platform", Dir: "/src/platform", Branch: "base", IsMain: true}}
	var output bytes.Buffer

	if err := printWorktreeCheckouts(&output, map[string]map[string]workspace.Worktree{"base": mainCheckout}, "/home/user"); err != nil {
		t.Fatal(err)
	}

	if output.Len() != 0 {
		t.Errorf("table = %q, want nothing", output.String())
	}
}
