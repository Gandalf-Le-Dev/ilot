package ilotctl

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
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
				if isRequested[instance{stackName: appInstance.stackName, appName: dependency}] || controller.Config.Apps[dependency].Scope == config.ScopeStack {
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

// The apps of a stack use its own copy of an app when it runs one, and the copy of the base stack otherwise.
// The scope of the app can fix the answer: always the own stack, or always the base stack.
func (controller *Controller) stackServing(stacks stackSet, stackName, appName string) string {
	app, _ := controller.anyDeclaration(stackName, appName)
	switch {
	case app.Scope == config.ScopeBase:
		return workspace.BaseStackName
	case app.Scope == config.ScopeStack || stacks.isRunning(instance{stackName: stackName, appName: appName}):
		return stackName
	}
	return workspace.BaseStackName
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
	for _, appInstance := range startOrder {
		appState := stacks.app(appInstance)
		for portName, fixedPort := range controller.fixedPortsOf(appInstance) {
			if !stacks.isRunning(appInstance) && !controller.IsPortFreeOnHost(fixedPort) {
				return fmt.Errorf("port %d of %s is taken by another process: stop it first (supervisor?)", fixedPort, appInstance.processName())
			}
			appState.Ports[portName] = fixedPort
		}
		var portNamesWithoutPort []string
		for _, portName := range controller.app(appInstance).PortNames() {
			if appState.Ports[portName] == 0 {
				portNamesWithoutPort = append(portNamesWithoutPort, portName)
			}
		}
		allocated, err := controller.PortRange.Allocate(len(portNamesWithoutPort), portsInUse, controller.IsPortFreeOnHost)
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
	if appInstance.stackName == workspace.BaseStackName {
		return controller.app(appInstance).FixedPorts
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
		environ, err := controller.shellEnviron()
		if err != nil {
			return err
		}
		releaseBuilds, err := controller.lockBuilds(ctx, appInstance.stackName)
		if err != nil {
			return err
		}
		fmt.Fprintf(controller.Output, "building %s in %s\n", appInstance.processName(), workingDir)
		stopProgress := controller.printProgressUntilStopped("building " + appInstance.processName())
		err = controller.RunBuild(ctx, workingDir, build.command, environ)
		stopProgress()
		if err := errors.Join(err, releaseBuilds()); err != nil {
			return fmt.Errorf("build %s: %w", appInstance.processName(), err)
		}
	}
	return nil
}

const buildLockPollInterval = 100 * time.Millisecond

// lockBuilds lets one build run in a stack at a time: two apps of a stack can write the same binary.
// ilot start, ilot restart and the watcher of an app all build through it.
func (controller *Controller) lockBuilds(ctx context.Context, stackName string) (release func() error, err error) {
	if err := os.MkdirAll(controller.Store.StackDir(stackName), 0o700); err != nil {
		return nil, err
	}
	lockFile, err := os.OpenFile(filepath.Join(controller.Store.StackDir(stackName), "build.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	hasSaidThatItWaits := false
	for {
		err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return lockFile.Close, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.Join(fmt.Errorf("lock %s: %w", lockFile.Name(), err), lockFile.Close())
		}
		if !hasSaidThatItWaits {
			fmt.Fprintf(controller.Output, "waiting for another build of stack %s\n", stackName)
			hasSaidThatItWaits = true
		}
		// A poll, and not a blocking lock, so that Ctrl-C ends the wait.
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), lockFile.Close())
		case <-time.After(buildLockPollInterval):
		}
	}
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

func (controller *Controller) loadStacks(stackName string) (stackSet, error) {
	stacks, err := controller.loadSavedStacks()
	if err != nil {
		return stackSet{}, err
	}
	for _, stackName := range []string{workspace.BaseStackName, stackName} {
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
	repoName := cmp.Or(controller.app(appInstance).Repo, controller.Workspace)
	checkoutDir := stacks.byName[appInstance.stackName].CheckoutDirs[repoName]
	if checkoutDir == "" {
		return "", fmt.Errorf("%s needs repo %s, which is not cloned: clone it, or set %s", appInstance.processName(), repoName, controller.repoPathKey(repoName))
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
