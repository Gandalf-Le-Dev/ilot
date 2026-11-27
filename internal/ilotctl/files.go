package ilotctl

import (
	"context"
	"maps"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
)

func (controller *Controller) applyAll(ctx context.Context, snapshot plan.Snapshot) error {
	var specs []ProcessSpec
	for _, runningInstance := range snapshot.Wanted() {
		spec, err := controller.processSpec(snapshot, runningInstance)
		if err != nil {
			return err
		}
		specs = append(specs, spec)
		checkoutDir, err := controller.checkoutDir(snapshot, runningInstance)
		if err != nil {
			return err
		}
		watcher, isWatched, err := controller.watcherSpec(snapshot, runningInstance, spec.Dir, checkoutDir)
		if err != nil {
			return err
		}
		if isWatched {
			specs = append(specs, watcher)
		}
	}
	if err := controller.Supervisor.Apply(ctx, specs); err != nil {
		return err
	}
	return controller.Supervisor.SetRoutes(ctx, controller.routes(snapshot))
}

// A stopped app keeps its route, so that its URL answers with the command that starts it.
func (controller *Controller) routes(snapshot plan.Snapshot) []daemon.Route {
	var routes []daemon.Route
	stacks := snapshot.State.Stacks(controller.Workspace)
	for _, stackName := range slices.Sorted(maps.Keys(stacks)) {
		for _, appName := range slices.Sorted(maps.Keys(stacks[stackName].Apps)) {
			routedInstance := plan.Instance{Stack: stackName, App: appName}
			app := snapshot.App(routedInstance)
			port := stacks[stackName].Apps[appName].Ports[app.Route]
			if app.Route == "" || port == 0 {
				continue
			}
			startHint := "ilot start " + appName
			if checkoutDir, err := controller.checkoutDir(snapshot, routedInstance); err == nil {
				startHint = "cd " + shellQuote(checkoutDir) + " && " + startHint
			}
			routes = append(routes, daemon.Route{Host: controller.publicHost(routedInstance), Port: port, HomePath: app.HomePath, StartHint: startHint})
		}
	}
	return routes
}
