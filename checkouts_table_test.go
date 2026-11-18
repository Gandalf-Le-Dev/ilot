package main

import (
	"bytes"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/checkouts"
)

func TestCheckoutsTableShowsOnlyTheWorktreesOfTicketStacks(t *testing.T) {
	mainCheckouts := map[string]checkouts.Checkout{
		"portal":   {Repo: "portal", Dir: "/home/user/acme-portal", Branch: "master", IsMainCheckout: true},
		"platform": {Repo: "platform", Dir: "/home/user/go/src/platform", Branch: "master", IsMainCheckout: true},
	}
	checkoutsByStack := map[string]map[string]checkouts.Checkout{
		"master": mainCheckouts,
		"devbox": mainCheckouts,
		"proj-0001": {
			"portal":   {Repo: "portal", Dir: "/home/user/worktrees-portal-0001", Branch: "PROJ-0001-x", IsMainCheckout: false},
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
	mainCheckout := map[string]checkouts.Checkout{"platform": {Repo: "platform", Dir: "/src/platform", Branch: "master", IsMainCheckout: true}}
	var output bytes.Buffer

	if err := printWorktreeCheckouts(&output, map[string]map[string]checkouts.Checkout{"master": mainCheckout}, "/home/user"); err != nil {
		t.Fatal(err)
	}

	if output.Len() != 0 {
		t.Errorf("table = %q, want nothing", output.String())
	}
}
