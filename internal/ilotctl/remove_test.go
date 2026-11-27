package ilotctl

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/state"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

func TestRemoveStopsAppsAndForgetsTheStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "internalapi")
	portOfRemovedStack := environment.appPort(t, "proj-1234", "internalapi")

	if err := environment.controller.Remove(t.Context(), "proj-1234"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	for processName := range environment.supervisor.runningSpecs {
		if strings.HasSuffix(processName, ".proj-1234") {
			t.Errorf("%s still runs", processName)
		}
	}
	for _, route := range environment.supervisor.routes {
		if strings.Contains(route.Host, ".proj-1234.") {
			t.Errorf("the removed stack keeps the route %s", route.Host)
		}
	}
	if _, err := os.Stat(environment.controller.Store.StackDir("proj-1234")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("folder of the removed stack: stat err = %v, want not exist", err)
	}
	otherStack := "proj-5678"
	environment.start(t, otherStack, StartOptions{}, "internalapi")
	if port := environment.appPort(t, "proj-5678", "internalapi"); port != portOfRemovedStack {
		t.Errorf("internalapi.proj-5678 got port %d, want the freed port %d", port, portOfRemovedStack)
	}
}

func TestRemoveRefusesMasterAndDevbox(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.master, StartOptions{}, "internalapi")

	for _, stackName := range []string{workspace.MasterStackName, workspace.SharedStackName} {
		err := environment.controller.Remove(t.Context(), stackName)
		if err == nil || !strings.Contains(err.Error(), "cannot remove") {
			t.Errorf("Remove(%s) = %v, want a refusal", stackName, err)
		}
	}
	if _, isRunning := environment.supervisor.runningSpecs["internalapi.master"]; !isRunning {
		t.Error("internalapi.master stopped after a refused remove")
	}
}

func TestRemoveRefusesAnUnknownStack(t *testing.T) {
	environment := newTestEnvironment(t)

	err := environment.controller.Remove(t.Context(), "proj-4040")

	if err == nil || !strings.Contains(err.Error(), `no stack "proj-4040"`) {
		t.Fatalf("Remove = %v, want an error naming the unknown stack", err)
	}
}

func saveEmptyStacks(t *testing.T, store state.Store, stackNames ...string) {
	t.Helper()
	for _, stackName := range stackNames {
		if err := store.Save(state.StackState{Name: stackName, CheckoutDirs: map[string]string{}, Apps: map[string]state.AppState{}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStacksWithoutCheckoutsListsOnlyGoneOnes(t *testing.T) {
	environment := newTestEnvironment(t)
	saveEmptyStacks(t, environment.controller.Store, "master", "devbox", "proj-1234", "proj-9999", "task-1")

	gone, err := environment.controller.StacksWithoutCheckouts()

	if err != nil {
		t.Fatalf("StacksWithoutCheckouts: %v", err)
	}
	if want := []string{"proj-9999", "task-1"}; !slices.Equal(gone, want) {
		t.Errorf("StacksWithoutCheckouts = %v, want %v", gone, want)
	}
}

func TestStacksWithoutCheckoutsKeepsAStackWithSeveralWorktreesOfARepo(t *testing.T) {
	environment := newTestEnvironment(t)
	saveEmptyStacks(t, environment.controller.Store, "proj-2001")
	environment.controller.CheckoutsForStack = func(string) (map[string]workspace.Worktree, error) {
		return map[string]workspace.Worktree{}, workspace.SeveralWorktreesError{Repo: "platform", StackName: "proj-2001", WorktreeDirs: []string{"/a", "/b"}}
	}

	gone, err := environment.controller.StacksWithoutCheckouts()

	if err != nil || len(gone) != 0 {
		t.Fatalf("StacksWithoutCheckouts = %v, %v, want no stack and no error", gone, err)
	}
}

func TestStacksWithoutCheckoutsFailsWhenGitFails(t *testing.T) {
	environment := newTestEnvironment(t)
	saveEmptyStacks(t, environment.controller.Store, "proj-2001")
	environment.controller.CheckoutsForStack = func(string) (map[string]workspace.Worktree, error) {
		return nil, errors.New("list the worktrees of platform: exit status 128")
	}

	if _, err := environment.controller.StacksWithoutCheckouts(); err == nil {
		t.Fatal("StacksWithoutCheckouts hid a git failure")
	}
}

func TestStackCheckoutsGivesTheCheckoutOfEachRepoPerStack(t *testing.T) {
	environment := newTestEnvironment(t)
	saveEmptyStacks(t, environment.controller.Store, "master", "proj-1234")

	checkoutsByStack, err := environment.controller.StackCheckouts()

	if err != nil {
		t.Fatalf("StackCheckouts: %v", err)
	}
	if dir := checkoutsByStack["proj-1234"]["platform"].Dir; dir != "/worktrees/proj-1234" {
		t.Errorf("platform checkout of proj-1234 = %q", dir)
	}
	if dir := checkoutsByStack["proj-1234"]["portal"].Dir; dir != "/src/portal" {
		t.Errorf("portal checkout of proj-1234 = %q", dir)
	}
	if _, isListed := checkoutsByStack["master"]; !isListed {
		t.Error("master missing")
	}
}
