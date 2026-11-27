package main

import (
	"bytes"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

func TestCheckoutsTableShowsOnlyTheWorktreesOfFeatureStacks(t *testing.T) {
	mainCheckouts := map[string]workspace.Worktree{
		"studio":   {Repo: "studio", Dir: "/home/user/acme-studio", Branch: "main", IsMain: true},
		"payments": {Repo: "payments", Dir: "/home/user/go/src/payments", Branch: "main", IsMain: true},
	}
	checkoutsByStack := map[string]map[string]workspace.Worktree{
		"base": mainCheckouts,
		"task-0001": {
			"studio":   {Repo: "studio", Dir: "/home/user/worktrees-studio-0001", Branch: "TASK-0001-x", IsMain: false},
			"payments": mainCheckouts["payments"],
		},
		"task-7": mainCheckouts,
	}
	var output bytes.Buffer

	if err := printWorktreeCheckouts(&output, checkoutsByStack, "/home/user"); err != nil {
		t.Fatal(err)
	}

	want := "\nCHECKOUT   REPO    FOLDER                   BRANCH\n" +
		"task-0001  studio  ~/worktrees-studio-0001  TASK-0001-x\n"
	if output.String() != want {
		t.Errorf("table =\n%s\nwant\n%s", output.String(), want)
	}
}

func TestCheckoutsTableIsEmptyWithoutWorktrees(t *testing.T) {
	mainCheckout := map[string]workspace.Worktree{"payments": {Repo: "payments", Dir: "/src/payments", Branch: "base", IsMain: true}}
	var output bytes.Buffer

	if err := printWorktreeCheckouts(&output, map[string]map[string]workspace.Worktree{"base": mainCheckout}, "/home/user"); err != nil {
		t.Fatal(err)
	}

	if output.Len() != 0 {
		t.Errorf("table = %q, want nothing", output.String())
	}
}
