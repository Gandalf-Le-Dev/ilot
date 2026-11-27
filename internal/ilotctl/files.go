package ilotctl

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/proxyfiles"
)

func (controller *Controller) applyAll(ctx context.Context, stacks stackSet) error {
	if err := controller.writeRoutesFiles(stacks); err != nil {
		return err
	}
	var specs []ProcessSpec
	for _, runningInstance := range stacks.runningInstances() {
		spec, err := controller.processSpec(stacks, runningInstance)
		if err != nil {
			return err
		}
		specs = append(specs, spec)
		watcher, isWatched, err := controller.watcherSpec(stacks, runningInstance, spec.Dir)
		if err != nil {
			return err
		}
		if isWatched {
			specs = append(specs, watcher)
		}
	}
	return controller.Supervisor.Apply(ctx, specs)
}

func (controller *Controller) writeRoutesFiles(stacks stackSet) error {
	for _, stackName := range slices.Sorted(maps.Keys(stacks.byName)) {
		var routedApps []proxyfiles.RoutedApp
		for appName, appState := range stacks.byName[stackName].Apps {
			routedInstance := instance{stackName: stackName, appName: appName}
			if route := controller.app(routedInstance).Route; route != "" && appState.Ports[route] != 0 {
				routedApps = append(routedApps, proxyfiles.RoutedApp{Name: appName, Host: controller.routeHost(routedInstance), Port: appState.Ports[route], HomePath: controller.app(routedInstance).HomePath})
			}
		}
		routesFile := filepath.Join(controller.RoutesDir, proxyfiles.RoutesFileName(stackName))
		if len(routedApps) == 0 {
			if err := os.Remove(routesFile); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		content, err := proxyfiles.TraefikRoutes(stackName, controller.Config.Domain, controller.BackendHost, routedApps)
		if err != nil {
			return err
		}
		if err := writeFileIfChanged(routesFile, content); err != nil {
			return err
		}
	}
	return nil
}

func writeFileIfChanged(path string, content []byte) error {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}
