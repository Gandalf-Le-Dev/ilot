package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

type Listing struct {
	Apps             []AppRow             `json:"apps"`
	ExternalServices []ExternalServiceRow `json:"external_services"`
	// The command that starts again the members that the daemon does not run, or nothing when every member runs.
	Restore string `json:"restore,omitempty"`
}

// The status of a member that the daemon does not hold, after a crash or a restart of the daemon.
const statusDown = "down"

type AppRow struct {
	Stack  string         `json:"stack"`
	App    string         `json:"app"`
	Status string         `json:"status"`
	Ports  map[string]int `json:"ports"`
	URL    string         `json:"url"`
	// No ilot.yaml of its stack declares the app any more. It keeps its ports until ilot stop removes it.
	Undeclared bool `json:"undeclared,omitzero"`
}

type ExternalServiceRow struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Port   int    `json:"port"`
}

func (executor *Executor) List(ctx context.Context) (Listing, error) {
	current, err := executor.loadState()
	if err != nil {
		return Listing{}, err
	}
	snapshot := plan.Snapshot{Workspace: executor.Workspace, Own: plan.BaseStack, State: current, Configs: executor.readableConfigs(current), Machine: executor.Machine}
	processByName, err := executor.Supervisor.Processes(ctx)
	if err != nil {
		return Listing{}, err
	}
	listing := Listing{Apps: []AppRow{}, ExternalServices: []ExternalServiceRow{}}
	stacks := current.Stacks(executor.Workspace)
	for _, stackName := range slices.Sorted(maps.Keys(stacks)) {
		for _, appName := range slices.Sorted(maps.Keys(stacks[stackName].Apps)) {
			appInstance := plan.Instance{Stack: stackName, App: appName}
			status := "stopped"
			process, isKnown := processByName[snapshot.ProcessName(appInstance)]
			undeclared := isUndeclared(snapshot, appInstance)
			switch {
			case isKnown:
				status = statusOf(process)
			case stacks[stackName].Apps[appName].Wanted:
				status = statusDown
				// ilot start --all does not start an app that no ilot.yaml declares.
				if !undeclared {
					listing.Restore = "ilot start --all"
				}
			}
			listing.Apps = append(listing.Apps, AppRow{
				Stack:      stackName,
				App:        appName,
				Status:     status,
				Ports:      stacks[stackName].Apps[appName].Ports,
				URL:        snapshot.URL(appInstance),
				Undeclared: undeclared,
			})
		}
	}
	if executor.ConfigErr != nil {
		fmt.Fprintf(executor.Output, "warning: the external services are not listed: %v\n", executor.ConfigErr)
	}
	for _, externalName := range slices.Sorted(maps.Keys(executor.Config.External)) {
		port := executor.Config.External[externalName].Port
		status := "down"
		if executor.IsExternalServiceUp(port) {
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

func (executor *Executor) StackCheckouts() (map[string]map[string]workspace.Worktree, error) {
	current, err := executor.loadState()
	if err != nil {
		return nil, err
	}
	checkoutsByStack := map[string]map[string]workspace.Worktree{}
	for stackName := range current.Stacks(executor.Workspace) {
		checkoutByRepo, err := executor.CheckoutsForStack(stackName)
		if err != nil {
			return nil, err
		}
		checkoutsByStack[stackName] = checkoutByRepo
	}
	return checkoutsByStack, nil
}

func (executor *Executor) printURLs(snapshot plan.Snapshot, requestedInstances []plan.Instance) {
	data := executor.Data
	if data == nil {
		data = executor.Output
	}
	for _, appInstance := range requestedInstances {
		if url := snapshot.URL(appInstance); url != "" && snapshot.IsWanted(appInstance) {
			fmt.Fprintf(data, "%s: %s\n", plan.ProcessName(executor.Workspace, appInstance), url)
		}
	}
}

// readableConfigs gives the configuration of each stack that has one that parses. A listing works without them.
func (executor *Executor) readableConfigs(current state.State) map[string]config.Config {
	configByStack := map[string]config.Config{}
	if executor.ConfigErr == nil {
		configByStack[plan.BaseStack] = executor.Config
	}
	for stackName := range current.Stacks(executor.Workspace) {
		if executor.ConfigForStack == nil {
			if executor.ConfigErr == nil {
				configByStack[stackName] = executor.Config
			}
			continue
		}
		if stackConfig, err := executor.ConfigForStack(stackName); err == nil {
			configByStack[stackName] = stackConfig
		}
	}
	return configByStack
}

// An instance of a stack whose configuration cannot be read is never called undeclared: nothing says so.
func isUndeclared(snapshot plan.Snapshot, appInstance plan.Instance) bool {
	if _, isLoaded := snapshot.Configs[appInstance.Stack]; !isLoaded {
		return false
	}
	_, isDeclared := snapshot.Declaration(appInstance)
	return !isDeclared
}
