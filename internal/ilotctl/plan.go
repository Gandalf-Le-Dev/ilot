package ilotctl

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/appconfig"
	"github.com/Gandalf-Le-Dev/ilot/internal/ports"
	"github.com/Gandalf-Le-Dev/ilot/internal/stack"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

func (controller *Controller) planStart(stacks stackSet, requestedInstances []instance, options StartOptions) ([]instance, []string) {
	isRequested := map[instance]bool{}
	for _, requestedInstance := range requestedInstances {
		isRequested[requestedInstance] = true
	}
	var startOrder []instance
	isPlanned := map[instance]bool{}
	isExternalDependency := map[string]bool{}
	var plan func(appInstance instance)
	plan = func(appInstance instance) {
		if isPlanned[appInstance] {
			return
		}
		isPlanned[appInstance] = true
		if !options.SkipDependencies {
			for _, dependency := range controller.app(appInstance).DependsOn {
				if _, isExternal := controller.Config.External[dependency]; isExternal {
					isExternalDependency[dependency] = true
					continue
				}
				dependencyStack := controller.stackServing(stacks, appInstance.stackName, dependency)
				if isRequested[instance{stackName: appInstance.stackName, appName: dependency}] || controller.isLinkedContainer(appInstance, dependency) || controller.Config.Apps[dependency].PerStack {
					dependencyStack = appInstance.stackName
				}
				plan(instance{stackName: dependencyStack, appName: dependency})
			}
		}
		startOrder = append(startOrder, appInstance)
	}
	for _, requestedInstance := range requestedInstances {
		plan(requestedInstance)
	}
	return startOrder, slices.Sorted(maps.Keys(isExternalDependency))
}

// The apps of a stack use its own instance of an app when it runs one (per_stack apps always), master's otherwise.
func (controller *Controller) stackServing(stacks stackSet, stackName, appName string) string {
	app, _ := controller.anyDeclaration(stackName, appName)
	switch {
	case app.Shared:
		return stack.SharedStackName
	case app.PerStack || stacks.isRunning(instance{stackName: stackName, appName: appName}):
		return stackName
	}
	return stack.MasterStackName
}

func (controller *Controller) checkExternalServicesUp(externalNames []string) error {
	var problems []error
	for _, externalName := range externalNames {
		externalService := controller.Config.External[externalName]
		if !controller.IsExternalServiceUp(externalService.Port) {
			problems = append(problems, fmt.Errorf("external service %s is down on port %d, start it with: %s", externalName, externalService.Port, externalService.StartHint))
		}
	}
	return errors.Join(problems...)
}

func (controller *Controller) allocatePorts(stacks stackSet, startOrder []instance) error {
	portsInUse := stacks.portsInUse()
	reservedRange := ports.Range{First: appconfig.ReservedPortFirst, Last: appconfig.ReservedPortLast}
	for _, appInstance := range startOrder {
		appState := stacks.app(appInstance)
		for portName, fixedPort := range controller.fixedPortsOf(appInstance) {
			if !controller.isContainer(appInstance) && !stacks.isRunning(appInstance) && !controller.IsPortFreeOnHost(fixedPort) {
				return fmt.Errorf("port %d of %s is taken by another process: stop it first (supervisor?)", fixedPort, appInstance.processName())
			}
			appState.Ports[portName] = fixedPort
		}
		var portNamesWithoutPort []string
		for _, portName := range controller.app(appInstance).Ports {
			if appState.Ports[portName] == 0 {
				portNamesWithoutPort = append(portNamesWithoutPort, portName)
			}
		}
		allocated, err := reservedRange.Allocate(len(portNamesWithoutPort), portsInUse, controller.IsPortFreeOnHost)
		if err != nil {
			return err
		}
		for index, portName := range portNamesWithoutPort {
			appState.Ports[portName] = allocated[index]
			portsInUse[allocated[index]] = true
		}
		stacks.setApp(appInstance, appState)
	}
	return nil
}

func (controller *Controller) fixedPortsOf(appInstance instance) map[string]int {
	app := controller.app(appInstance)
	if app.Shared || appInstance.stackName == stack.MasterStackName || controller.isContainer(appInstance) {
		return app.FixedPorts
	}
	return nil
}

func (controller *Controller) checkEveryProcessRenders(stacks stackSet, startOrder []instance) error {
	stacksWithPlanRunning := stacks.clone()
	for _, appInstance := range startOrder {
		stacksWithPlanRunning.setRunning(appInstance, true)
	}
	for _, appInstance := range startOrder {
		if _, err := controller.processSpec(stacksWithPlanRunning, appInstance); err != nil {
			return err
		}
	}
	return nil
}

func (controller *Controller) buildApps(ctx context.Context, stacks stackSet, startOrder []instance, needsBuild func(instance) bool) error {
	type buildInWorktree struct {
		workingDir string
		command    string
	}
	isBuilt := map[buildInWorktree]bool{}
	for _, appInstance := range startOrder {
		app := controller.app(appInstance)
		if app.Build == "" || !needsBuild(appInstance) {
			continue
		}
		buildCommand, err := controller.renderTemplate(stacks, appInstance, app.Build)
		if err != nil {
			return err
		}
		workingDir, err := controller.checkoutDir(stacks, appInstance)
		if err != nil {
			return err
		}
		build := buildInWorktree{workingDir: workingDir, command: controller.wrapped(appInstance, buildCommand)}
		if isBuilt[build] {
			continue
		}
		isBuilt[build] = true
		if err := os.MkdirAll(controller.binDir(appInstance.stackName), 0o755); err != nil {
			return err
		}
		fmt.Fprintf(controller.Output, "building %s in %s\n", appInstance.processName(), workingDir)
		environ, err := controller.shellEnviron()
		if err != nil {
			return err
		}
		stopProgress := controller.printProgressUntilStopped("building " + appInstance.processName())
		err = controller.RunBuild(ctx, workingDir, build.command, environ)
		stopProgress()
		if err != nil {
			return fmt.Errorf("build %s: %w", appInstance.processName(), err)
		}
	}
	return nil
}

func (controller *Controller) loadSavedStacks() (stackSet, error) {
	allStacks, err := controller.Store.All()
	if err != nil {
		return stackSet{}, err
	}
	stacks := stackSet{byName: map[string]*state.StackState{}}
	for _, stackState := range allStacks {
		stacks.byName[stackState.Name] = &stackState
	}
	return stacks, nil
}

func (controller *Controller) loadStacks(worktree stack.Worktree) (stackSet, error) {
	stacks, err := controller.loadSavedStacks()
	if err != nil {
		return stackSet{}, err
	}
	for _, stackName := range []string{stack.MasterStackName, stack.SharedStackName, worktree.Name} {
		checkoutByRepo, err := controller.CheckoutsForStack(stackName)
		if err != nil {
			return stackSet{}, err
		}
		stackState := stacks.ensure(stackName)
		for repoName, checkout := range checkoutByRepo {
			stackState.CheckoutDirs[repoName] = checkout.Dir
		}
	}
	return stacks, controller.prepareStacks(stacks)
}

func (controller *Controller) checkoutDir(stacks stackSet, appInstance instance) (string, error) {
	repoName := cmp.Or(controller.app(appInstance).Repo, appconfig.DefaultRepo)
	checkoutDir := stacks.byName[appInstance.stackName].CheckoutDirs[repoName]
	if checkoutDir == "" {
		return "", fmt.Errorf("%s needs repo %s, which is not cloned: clone it, or set repos.%s.path in ~/.config/ilot/env.yaml", appInstance.processName(), repoName, repoName)
	}
	return checkoutDir, nil
}

func (controller *Controller) saveStacks(stacks stackSet) error {
	for _, stackName := range slices.Sorted(maps.Keys(stacks.byName)) {
		if err := controller.Store.Save(*stacks.byName[stackName]); err != nil {
			return err
		}
	}
	return nil
}
