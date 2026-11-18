package ilotctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/processcompose"
	"github.com/Gandalf-Le-Dev/ilot/internal/proxyfiles"
)

func (controller *Controller) applyAll(ctx context.Context, stacks stackSet) error {
	gatewayStacksWithNewAliases, err := controller.writeGatewayFiles(stacks)
	if err != nil {
		return err
	}
	if err := controller.writeRoutesFiles(stacks); err != nil {
		return err
	}
	if err := controller.restartGatewaysUntilReady(ctx, gatewayStacksWithNewAliases); err != nil {
		return err
	}
	var specs []processcompose.ProcessSpec
	for _, runningInstance := range stacks.runningInstances() {
		spec, err := controller.processSpec(stacks, runningInstance)
		if err != nil {
			return err
		}
		specs = append(specs, spec)
		watcher, isWatched, err := controller.watcherSpec(stacks, runningInstance, spec.WorkingDir)
		if err != nil {
			return err
		}
		if isWatched {
			specs = append(specs, watcher)
		}
	}
	return controller.Supervisor.Apply(ctx, specs)
}

// Before the update that adds an app: an app started while its gateway restarts fails its first calls.
func (controller *Controller) restartGatewaysUntilReady(ctx context.Context, gatewayStackNames []string) error {
	statusByProcess, err := controller.Supervisor.Statuses(ctx)
	if err != nil {
		return err
	}
	gatewayAppName := controller.Config.GatewayAppName()
	for _, stackName := range gatewayStackNames {
		gatewayProcessName := instance{stackName: stackName, appName: gatewayAppName}.processName()
		if !statusByProcess[gatewayProcessName].IsRunning {
			continue
		}
		if err := controller.Supervisor.Restart(ctx, gatewayProcessName); err != nil {
			return err
		}
		if err := controller.Supervisor.WaitReady(ctx, gatewayProcessName, controller.ReadyTimeout); err != nil {
			return err
		}
	}
	return nil
}

func (controller *Controller) writeGatewayFiles(stacks stackSet) (stacksWithChangedFile []string, err error) {
	if controller.Config.GatewayAppName() == "" {
		return nil, nil
	}
	urlByBaseAlias, err := controller.readGatewayBaseFile()
	if err != nil {
		return nil, err
	}
	for _, stackName := range slices.Sorted(maps.Keys(stacks.byName)) {
		urlByAlias := maps.Clone(urlByBaseAlias)
		for _, externalService := range controller.Config.External {
			for _, alias := range externalService.GatewayAliases {
				urlByAlias[alias] = fmt.Sprintf("http://localhost:%d", externalService.Port)
			}
		}
		for _, appName := range controller.appNamesOf(stacks, stackName) {
			servingInstance := instance{stackName: controller.stackServing(stacks, stackName, appName), appName: appName}
			app := controller.app(servingInstance)
			if len(app.GatewayAliases) == 0 || !stacks.isRunning(servingInstance) {
				continue
			}
			for _, alias := range app.GatewayAliases {
				gatewayInstance := instance{stackName: stackName, appName: controller.Config.GatewayAppName()}
				urlByAlias[alias] = fmt.Sprintf("http://%s:%d", controller.hostFor(gatewayInstance, servingInstance), stacks.app(servingInstance).Ports[app.Route])
			}
		}
		content, err := proxyfiles.LocalServices(urlByAlias)
		if err != nil {
			return nil, err
		}
		isChanged, err := writeFileIfChanged(filepath.Join(controller.Store.StackDir(stackName), "services.json"), content)
		if err != nil {
			return nil, err
		}
		if isChanged {
			stacksWithChangedFile = append(stacksWithChangedFile, stackName)
		}
	}
	return stacksWithChangedFile, nil
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
		if _, err := writeFileIfChanged(routesFile, content); err != nil {
			return err
		}
	}
	return nil
}

func (controller *Controller) readGatewayBaseFile() (map[string]string, error) {
	urlByAlias := map[string]string{}
	if controller.Config.GatewayBaseFile == "" {
		return urlByAlias, nil
	}
	content, err := os.ReadFile(controller.Config.GatewayBaseFile)
	if errors.Is(err, os.ErrNotExist) {
		return urlByAlias, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(content, &urlByAlias); err != nil {
		return nil, fmt.Errorf("read gateway base file %s: %w", controller.Config.GatewayBaseFile, err)
	}
	return urlByAlias, nil
}

func writeFileIfChanged(path string, content []byte) (bool, error) {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, content, 0o644)
}
