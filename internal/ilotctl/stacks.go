package ilotctl

import (
	"maps"
	"slices"
)

type stackSet struct {
	byName map[string]*StackState
}

func (stacks stackSet) ensure(stackName string) *StackState {
	stackState, isKnown := stacks.byName[stackName]
	if !isKnown {
		stackState = &StackState{Name: stackName, CheckoutDirs: map[string]string{}, Apps: map[string]AppState{}}
		stacks.byName[stackName] = stackState
	}
	return stackState
}

func (stacks stackSet) app(appInstance instance) AppState {
	stackState, isKnown := stacks.byName[appInstance.stackName]
	if !isKnown {
		return AppState{Ports: map[string]int{}, Running: false}
	}
	appState := stackState.Apps[appInstance.appName]
	if appState.Ports == nil {
		appState.Ports = map[string]int{}
	}
	return appState
}

func (stacks stackSet) setApp(appInstance instance, appState AppState) {
	stacks.byName[appInstance.stackName].Apps[appInstance.appName] = appState
}

func (stacks stackSet) isRunning(appInstance instance) bool {
	return stacks.app(appInstance).Running
}

func (stacks stackSet) setRunning(appInstance instance, isRunning bool) {
	appState := stacks.app(appInstance)
	appState.Running = isRunning
	stacks.setApp(appInstance, appState)
}

func (stacks stackSet) runningInstances() []instance {
	var running []instance
	for _, stackName := range slices.Sorted(maps.Keys(stacks.byName)) {
		for _, appName := range slices.Sorted(maps.Keys(stacks.byName[stackName].Apps)) {
			if stacks.byName[stackName].Apps[appName].Running {
				running = append(running, instance{stackName: stackName, appName: appName})
			}
		}
	}
	return running
}

func (stacks stackSet) portsInUse() map[int]bool {
	portsInUse := map[int]bool{}
	for _, stackState := range stacks.byName {
		for _, appState := range stackState.Apps {
			for _, port := range appState.Ports {
				portsInUse[port] = true
			}
		}
	}
	return portsInUse
}

func (stacks stackSet) clone() stackSet {
	cloned := stackSet{byName: map[string]*StackState{}}
	for stackName, stackState := range stacks.byName {
		clonedApps := map[string]AppState{}
		for appName, appState := range stackState.Apps {
			clonedApps[appName] = AppState{Ports: maps.Clone(appState.Ports), Running: appState.Running}
		}
		cloned.byName[stackName] = &StackState{Name: stackState.Name, CheckoutDirs: maps.Clone(stackState.CheckoutDirs), BoundWorktrees: maps.Clone(stackState.BoundWorktrees), Apps: clonedApps}
	}
	return cloned
}
