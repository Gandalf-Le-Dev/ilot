package ilotctl

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

func (controller *Controller) loadStackConfigs(stackNames []string) error {
	return errors.Join(controller.loadEveryReadableStackConfig(stackNames)...)
}

// A stack whose config cannot be read keeps the config of the command, so a listing still shows it.
func (controller *Controller) loadEveryReadableStackConfig(stackNames []string) []error {
	controller.configByStack = map[string]config.Config{}
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

func (controller *Controller) configOf(stackName string) config.Config {
	if config, isLoaded := controller.configByStack[stackName]; isLoaded {
		return config
	}
	return controller.Config
}

// An app with the scope base runs in the base stack, but the checkout of any stack can declare it.
func (controller *Controller) declaringConfig(appInstance instance) (config.Config, bool) {
	if _, isDeclared := controller.configOf(appInstance.stackName).Apps[appInstance.appName]; isDeclared {
		return controller.configOf(appInstance.stackName), true
	}
	if appInstance.stackName != workspace.BaseStackName {
		return config.Config{}, false
	}
	declaringConfig, isDeclared := controller.anyDeclaringConfig(appInstance.stackName, appInstance.appName)
	if !isDeclared || declaringConfig.Apps[appInstance.appName].Scope != config.ScopeBase {
		return config.Config{}, false
	}
	return declaringConfig, true
}

// For an app that a stack calls: its own config first, then the config of the command, then the other stacks.
func (controller *Controller) anyDeclaringConfig(stackName, appName string) (config.Config, bool) {
	candidates := []config.Config{controller.configOf(stackName), controller.Config}
	for _, loadedStackName := range slices.Sorted(maps.Keys(controller.configByStack)) {
		candidates = append(candidates, controller.configByStack[loadedStackName])
	}
	for _, candidate := range candidates {
		if _, isDeclared := candidate.Apps[appName]; isDeclared {
			return candidate, true
		}
	}
	return config.Config{}, false
}

func (controller *Controller) anyDeclaration(stackName, appName string) (config.App, bool) {
	config, isDeclared := controller.anyDeclaringConfig(stackName, appName)
	return config.Apps[appName], isDeclared
}

func (controller *Controller) lookupApp(appInstance instance) (config.App, bool) {
	config, isDeclared := controller.declaringConfig(appInstance)
	return config.Apps[appInstance.appName], isDeclared
}

func (controller *Controller) app(appInstance instance) config.App {
	app, _ := controller.lookupApp(appInstance)
	return app
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
