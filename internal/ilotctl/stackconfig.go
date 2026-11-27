package ilotctl

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/appconfig"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

func (controller *Controller) loadStackConfigs(stackNames []string) error {
	return errors.Join(controller.loadEveryReadableStackConfig(stackNames)...)
}

// A stack whose config cannot be read keeps the config of the command, so a listing still shows it.
func (controller *Controller) loadEveryReadableStackConfig(stackNames []string) []error {
	controller.configByStack = map[string]appconfig.Config{}
	if controller.ConfigForStack == nil {
		return nil
	}
	var problems []error
	for _, stackName := range stackNames {
		config, err := controller.ConfigForStack(stackName)
		if err != nil {
			problems = append(problems, fmt.Errorf("read the config of stack %s: %w: fix it, or remove the stack with ilot rm %s", stackName, err, stackName))
			continue
		}
		controller.configByStack[stackName] = config
	}
	return problems
}

func (controller *Controller) configOf(stackName string) appconfig.Config {
	if config, isLoaded := controller.configByStack[stackName]; isLoaded {
		return config
	}
	return controller.Config
}

// A shared app runs in the devbox stack, but the checkout of any stack can declare it.
func (controller *Controller) declaringConfig(appInstance instance) (appconfig.Config, bool) {
	if _, isDeclared := controller.configOf(appInstance.stackName).Apps[appInstance.appName]; isDeclared {
		return controller.configOf(appInstance.stackName), true
	}
	if appInstance.stackName != workspace.SharedStackName {
		return appconfig.Config{}, false
	}
	return controller.anyDeclaringConfig(appInstance.stackName, appInstance.appName)
}

// For an app that a stack calls: its own config first, then the config of the command, then the other stacks.
func (controller *Controller) anyDeclaringConfig(stackName, appName string) (appconfig.Config, bool) {
	candidates := []appconfig.Config{controller.configOf(stackName), controller.Config}
	for _, loadedStackName := range slices.Sorted(maps.Keys(controller.configByStack)) {
		candidates = append(candidates, controller.configByStack[loadedStackName])
	}
	for _, candidate := range candidates {
		if _, isDeclared := candidate.Apps[appName]; isDeclared {
			return candidate, true
		}
	}
	return appconfig.Config{}, false
}

func (controller *Controller) anyDeclaration(stackName, appName string) (appconfig.App, bool) {
	config, isDeclared := controller.anyDeclaringConfig(stackName, appName)
	return config.Apps[appName], isDeclared
}

func (controller *Controller) lookupApp(appInstance instance) (appconfig.App, bool) {
	config, isDeclared := controller.declaringConfig(appInstance)
	return config.Apps[appInstance.appName], isDeclared
}

func (controller *Controller) app(appInstance instance) appconfig.App {
	app, _ := controller.lookupApp(appInstance)
	return app
}

func (controller *Controller) appNamesOf(stacks stackSet, stackName string) []string {
	appNames := slices.Collect(maps.Keys(controller.configOf(stackName).Apps))
	for _, sharedInstance := range stacks.runningInstances() {
		if sharedInstance.stackName == workspace.SharedStackName && !slices.Contains(appNames, sharedInstance.appName) {
			appNames = append(appNames, sharedInstance.appName)
		}
	}
	slices.Sort(appNames)
	return appNames
}

func (controller *Controller) prepareStacks(stacks stackSet) error {
	if err := controller.loadStackConfigs(slices.Sorted(maps.Keys(stacks.byName))); err != nil {
		return err
	}
	for _, runningInstance := range stacks.runningInstances() {
		if _, isDeclared := controller.lookupApp(runningInstance); !isDeclared {
			stacks.setRunning(runningInstance, false)
			fmt.Fprintf(controller.Output, "warning: %s stopped: no ilot.yaml of stack %s declares it any more\n", runningInstance.processName(), runningInstance.stackName)
		}
	}
	return nil
}

func (controller *Controller) repoEnvOf(appInstance instance) map[string]map[string]string {
	config, _ := controller.declaringConfig(appInstance)
	return config.RepoEnv
}
