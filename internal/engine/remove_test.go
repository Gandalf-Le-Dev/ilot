package engine

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

func TestRemoveStopsAppsAndForgetsTheStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "internalapi")
	portOfRemovedStack := environment.appPort(t, "proj-1234", "internalapi")

	if err := environment.executor.Remove(t.Context(), "proj-1234"); err != nil {
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
	if _, err := os.Stat(environment.executor.StackDir("proj-1234")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("folder of the removed stack: stat err = %v, want not exist", err)
	}
	otherStack := "retry-jobs"
	environment.start(t, otherStack, StartOptions{}, "internalapi")
	if port := environment.appPort(t, "retry-jobs", "internalapi"); port != portOfRemovedStack {
		t.Errorf("internalapi.retry-jobs.shop got port %d, want the freed port %d", port, portOfRemovedStack)
	}
}

func TestRemoveRefusesTheBaseStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "internalapi")

	for _, stackName := range []string{workspace.BaseStackName} {
		err := environment.executor.Remove(t.Context(), stackName)
		if err == nil || !strings.Contains(err.Error(), "cannot remove") {
			t.Errorf("Remove(%s) = %v, want a refusal", stackName, err)
		}
	}
	if _, isRunning := environment.supervisor.runningSpecs["internalapi.base.shop"]; !isRunning {
		t.Error("internalapi.base.shop stopped after a refused remove")
	}
}

func TestRemoveRefusesAnUnknownStack(t *testing.T) {
	environment := newTestEnvironment(t)

	err := environment.executor.Remove(t.Context(), "proj-4040")

	if err == nil || !strings.Contains(err.Error(), `no stack "proj-4040"`) {
		t.Fatalf("Remove = %v, want an error naming the unknown stack", err)
	}
}

func saveStacks(t *testing.T, executor *Executor, bindingByStack map[string]map[string]string) {
	t.Helper()
	current, err := executor.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for stackName, binding := range bindingByStack {
		current.Stack(executor.Workspace, stackName).Checkouts = binding
	}
	if _, err := executor.Store.Save(current); err != nil {
		t.Fatal(err)
	}
}

func TestCleanRemovesOnlyAStackWhoseBoundWorktreesAreAllDeleted(t *testing.T) {
	environment := newTestEnvironment(t)
	existingWorktreeDir, otherExistingWorktreeDir := t.TempDir(), t.TempDir()
	deletedWorktreeDir := filepath.Join(t.TempDir(), "deleted")
	boundWorktreesByStack := map[string]map[string]string{
		"base":         nil,
		"all-deleted":  {"shop": deletedWorktreeDir, "portal": deletedWorktreeDir + "-portal"},
		"one-deleted":  {"shop": deletedWorktreeDir, "portal": existingWorktreeDir},
		"none-deleted": {"shop": otherExistingWorktreeDir},
		"moved-away":   nil,
	}
	saveStacks(t, environment.executor, boundWorktreesByStack)

	removable, err := environment.executor.StacksWithoutWorktrees()

	if want := []string{"all-deleted", "moved-away"}; err != nil || !slices.Equal(removable, want) {
		t.Errorf("StacksWithoutWorktrees = %v, %v; want %v", removable, err, want)
	}
}

func TestStackCheckoutsGivesTheCheckoutOfEachRepoPerStack(t *testing.T) {
	environment := newTestEnvironment(t)
	saveStacks(t, environment.executor, map[string]map[string]string{"base": nil, "proj-1234": nil})

	checkoutsByStack, err := environment.executor.StackCheckouts()

	if err != nil {
		t.Fatalf("StackCheckouts: %v", err)
	}
	if dir := checkoutsByStack["proj-1234"]["shop"].Dir; dir != "/worktrees/proj-1234" {
		t.Errorf("shop checkout of proj-1234 = %q", dir)
	}
	if dir := checkoutsByStack["proj-1234"]["portal"].Dir; dir != "/src/portal" {
		t.Errorf("portal checkout of proj-1234 = %q", dir)
	}
	if _, isListed := checkoutsByStack["base"]; !isListed {
		t.Error("base missing")
	}
}
