package ilotctl

import (
	"context"
	"maps"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
)

func (controller *Controller) applyAll(ctx context.Context, stacks stackSet) error {
	var specs []ProcessSpec
	for _, runningInstance := range stacks.runningInstances() {
		spec, err := controller.processSpec(stacks, runningInstance)
		if err != nil {
			return err
		}
		specs = append(specs, spec)
		checkoutDir, err := controller.checkoutDir(stacks, runningInstance)
		if err != nil {
			return err
		}
		watcher, isWatched, err := controller.watcherSpec(runningInstance, spec.Dir, checkoutDir)
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
	return controller.Supervisor.SetRoutes(ctx, controller.routes(stacks))
}

// A stopped app keeps its route, so that its URL answers with the command that starts it.
func (controller *Controller) routes(stacks stackSet) []daemon.Route {
	var routes []daemon.Route
	for _, stackName := range slices.Sorted(maps.Keys(stacks.byName)) {
		stackState := stacks.byName[stackName]
		for _, appName := range slices.Sorted(maps.Keys(stackState.Apps)) {
			routedInstance := instance{stackName: stackName, appName: appName}
			app := controller.app(routedInstance)
			port := stackState.Apps[appName].Ports[app.Route]
			if app.Route == "" || port == 0 {
				continue
			}
			startHint := "ilot start " + appName
			if checkoutDir, err := controller.checkoutDir(stacks, routedInstance); err == nil {
				startHint = "cd " + shellQuote(checkoutDir) + " && " + startHint
			}
			routes = append(routes, daemon.Route{Host: controller.publicHost(routedInstance), Port: port, HomePath: app.HomePath, StartHint: startHint})
		}
	}
	return routes
}
