package ilotctl

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

type Listing struct {
	Apps             []AppRow             `json:"apps"`
	ExternalServices []ExternalServiceRow `json:"external_services"`
}

type AppRow struct {
	Stack  string         `json:"stack"`
	App    string         `json:"app"`
	Status string         `json:"status"`
	Ports  map[string]int `json:"ports"`
	URL    string         `json:"url"`
}

type ExternalServiceRow struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Port   int    `json:"port"`
}

func (controller *Controller) List(ctx context.Context) (Listing, error) {
	current, err := controller.loadState()
	if err != nil {
		return Listing{}, err
	}
	snapshot, problems := controller.snapshotOf(current, plan.BaseStack)
	for _, problem := range problems {
		fmt.Fprintf(controller.Output, "warning: %v\n", problem)
	}
	processByName, err := controller.Supervisor.Processes(ctx)
	if err != nil {
		return Listing{}, err
	}
	listing := Listing{Apps: []AppRow{}, ExternalServices: []ExternalServiceRow{}}
	stacks := current.Stacks(controller.Workspace)
	for _, stackName := range slices.Sorted(maps.Keys(stacks)) {
		for _, appName := range slices.Sorted(maps.Keys(stacks[stackName].Apps)) {
			appInstance := plan.Instance{Stack: stackName, App: appName}
			status := "stopped"
			if process, isKnown := processByName[plan.ProcessName(controller.Workspace, appInstance)]; isKnown {
				status = statusOf(process)
			}
			listing.Apps = append(listing.Apps, AppRow{
				Stack:  stackName,
				App:    appName,
				Status: status,
				Ports:  stacks[stackName].Apps[appName].Ports,
				URL:    snapshot.URL(appInstance),
			})
		}
	}
	for _, externalName := range slices.Sorted(maps.Keys(controller.Config.External)) {
		port := controller.Config.External[externalName].Port
		status := "down"
		if controller.IsExternalServiceUp(port) {
			status = "up"
		}
		listing.ExternalServices = append(listing.ExternalServices, ExternalServiceRow{Name: externalName, Status: status, Port: port})
	}
	return listing, nil
}

func statusOf(process daemon.Process) string {
	if process.State == daemon.StateExited {
		return fmt.Sprintf("%s (%d)", process.State, process.ExitCode)
	}
	return string(process.State)
}

func (controller *Controller) StackCheckouts() (map[string]map[string]workspace.Worktree, error) {
	current, err := controller.loadState()
	if err != nil {
		return nil, err
	}
	checkoutsByStack := map[string]map[string]workspace.Worktree{}
	for stackName := range current.Stacks(controller.Workspace) {
		checkoutByRepo, err := controller.CheckoutsForStack(stackName)
		if err != nil {
			return nil, err
		}
		checkoutsByStack[stackName] = checkoutByRepo
	}
	return checkoutsByStack, nil
}

func (controller *Controller) printURLs(snapshot plan.Snapshot, requestedInstances []plan.Instance) {
	for _, appInstance := range requestedInstances {
		if url := snapshot.URL(appInstance); url != "" && snapshot.IsWanted(appInstance) {
			fmt.Fprintf(controller.Output, "%s: %s\n", plan.ProcessName(controller.Workspace, appInstance), url)
		}
	}
}
