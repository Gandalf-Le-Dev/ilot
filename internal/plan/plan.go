// Package plan decides what a command does, from a snapshot of the state and the configuration.
// It runs no process, reads no file and opens no socket, so a test needs no fake.
package plan

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

// BaseStack is the stack of the main checkouts.
const BaseStack = "base"

// Instance is one copy of an app: the app in one stack.
type Instance struct {
	Stack string
	App   string
}

// Snapshot is what a command knows when it plans.
type Snapshot struct {
	Workspace string
	// The stack of the command. A stack whose configuration is not loaded reads the configuration of this one.
	Own string
	// The state of every workspace, because a port is unique on the machine. The plan changes the membership and the ports of its own copy.
	State state.State
	// The configuration of each loaded stack.
	Configs map[string]config.Config
	// The checkout folder of each repo, by stack.
	Checkouts map[string]map[string]string
	// The path that each repo would have, for a repo that is not cloned.
	ReposNotCloned map[string]string
	// What the global file holds for the workspace: values for every app, and values for one app.
	Settings config.WorkspaceSettings
	Machine  Machine
}

// Clone gives a snapshot whose state can change without a change to this one.
func (snapshot Snapshot) Clone() Snapshot {
	cloned := snapshot
	cloned.State = snapshot.State.Clone()
	return cloned
}

func (snapshot Snapshot) configOf(stackName string) config.Config {
	if stackConfig, isLoaded := snapshot.Configs[stackName]; isLoaded {
		return stackConfig
	}
	return snapshot.Configs[snapshot.Own]
}

// DeclaringConfig gives the configuration that declares an instance.
// An app with the scope base runs in the base stack, but the checkout of any stack can declare it.
func (snapshot Snapshot) DeclaringConfig(instance Instance) (config.Config, bool) {
	if _, isDeclared := snapshot.configOf(instance.Stack).Apps[instance.App]; isDeclared {
		return snapshot.configOf(instance.Stack), true
	}
	if instance.Stack != BaseStack {
		return config.Config{}, false
	}
	declaringConfig, isDeclared := snapshot.anyDeclaringConfig(instance.Stack, instance.App)
	if !isDeclared || declaringConfig.Apps[instance.App].Scope != config.ScopeBase {
		return config.Config{}, false
	}
	return declaringConfig, true
}

// For an app that a stack calls: its own configuration first, then the one of the command, then the other stacks.
func (snapshot Snapshot) anyDeclaringConfig(stackName, appName string) (config.Config, bool) {
	candidates := []config.Config{snapshot.configOf(stackName), snapshot.Configs[snapshot.Own]}
	for _, loadedStackName := range slices.Sorted(maps.Keys(snapshot.Configs)) {
		candidates = append(candidates, snapshot.Configs[loadedStackName])
	}
	for _, candidate := range candidates {
		if _, isDeclared := candidate.Apps[appName]; isDeclared {
			return candidate, true
		}
	}
	return config.Config{}, false
}

// AnyDeclaration gives the declaration of an app that a stack calls, wherever it runs.
func (snapshot Snapshot) AnyDeclaration(stackName, appName string) (config.App, bool) {
	declaringConfig, isDeclared := snapshot.anyDeclaringConfig(stackName, appName)
	return declaringConfig.Apps[appName], isDeclared
}

// Declaration gives the app of an instance, as the configuration of its stack declares it.
func (snapshot Snapshot) Declaration(instance Instance) (config.App, bool) {
	declaringConfig, isDeclared := snapshot.DeclaringConfig(instance)
	return declaringConfig.Apps[instance.App], isDeclared
}

func (snapshot Snapshot) App(instance Instance) config.App {
	app, _ := snapshot.Declaration(instance)
	return app
}

func (snapshot Snapshot) External(name string) (config.ExternalService, bool) {
	externalService, isExternal := snapshot.configOf(snapshot.Own).External[name]
	return externalService, isExternal
}

// StackOf gives the stack where a command of a stack runs an app: the base stack for an app with the scope base.
func (snapshot Snapshot) StackOf(stackName, appName string) string {
	if snapshot.configOf(snapshot.Own).Apps[appName].Scope == config.ScopeBase {
		return BaseStack
	}
	return stackName
}

// Instances gives the instances that a command of a stack names.
func (snapshot Snapshot) Instances(stackName string, appNames []string) ([]Instance, error) {
	declared := snapshot.configOf(snapshot.Own).Apps
	var instances []Instance
	for _, appName := range appNames {
		if _, isApp := declared[appName]; !isApp {
			return nil, errors.Join(fmt.Errorf("unknown app %q, known apps: %v", appName, slices.Sorted(maps.Keys(declared))), snapshot.reposNotClonedHint())
		}
		instances = append(instances, Instance{Stack: snapshot.StackOf(stackName, appName), App: appName})
	}
	return instances, nil
}

func (snapshot Snapshot) reposNotClonedHint() error {
	var hints []error
	for _, repoName := range slices.Sorted(maps.Keys(snapshot.ReposNotCloned)) {
		hints = append(hints, fmt.Errorf("repo %s is not at %s: clone it, or set %s", repoName, snapshot.ReposNotCloned[repoName], snapshot.RepoPathKey(repoName)))
	}
	return errors.Join(hints...)
}

// RepoPathKey is where the user gives the path of a repo that is cloned at another place.
func (snapshot Snapshot) RepoPathKey(repoName string) string {
	return fmt.Sprintf("workspaces.%s.repos.%s in ~/%s", snapshot.Workspace, repoName, config.GlobalFilePath)
}

// IsWanted tells if a stack owns an app: from ilot start until ilot stop or ilot rm. A crash does not change it.
func (snapshot Snapshot) IsWanted(instance Instance) bool {
	return snapshot.State.App(snapshot.Workspace, instance.Stack, instance.App).Wanted
}

func (snapshot Snapshot) SetWanted(instance Instance, isWanted bool) {
	stack := snapshot.State.Stack(snapshot.Workspace, instance.Stack)
	app := stack.Apps[instance.App]
	app.Wanted = isWanted
	stack.Apps[instance.App] = app
}

// Ports gives the ports of an instance. The map is never nil.
func (snapshot Snapshot) Ports(instance Instance) map[string]int {
	ports := snapshot.State.App(snapshot.Workspace, instance.Stack, instance.App).Ports
	if ports == nil {
		return map[string]int{}
	}
	return ports
}

func (snapshot Snapshot) SetPort(instance Instance, portName string, port int) {
	stack := snapshot.State.Stack(snapshot.Workspace, instance.Stack)
	app := stack.Apps[instance.App]
	if app.Ports == nil {
		app.Ports = map[string]int{}
	}
	app.Ports[portName] = port
	stack.Apps[instance.App] = app
}

// Wanted gives every instance that a stack of the workspace owns, sorted.
func (snapshot Snapshot) Wanted() []Instance {
	var wanted []Instance
	stacks := snapshot.State.Stacks(snapshot.Workspace)
	for _, stackName := range slices.Sorted(maps.Keys(stacks)) {
		for _, appName := range slices.Sorted(maps.Keys(stacks[stackName].Apps)) {
			if stacks[stackName].Apps[appName].Wanted {
				wanted = append(wanted, Instance{Stack: stackName, App: appName})
			}
		}
	}
	return wanted
}

// Serving gives the stack whose copy of an app the apps of a stack use: their own copy when the stack owns one, and the copy of the base stack otherwise.
// The scope of the app can fix the answer: always the own stack, or always the base stack.
// The membership decides, and never the status of a process, so a crash does not move an app to another stack.
func (snapshot Snapshot) Serving(stackName, appName string) string {
	app, _ := snapshot.AnyDeclaration(stackName, appName)
	switch {
	case app.Scope == config.ScopeBase:
		return BaseStack
	case app.Scope == config.ScopeStack || snapshot.IsWanted(Instance{Stack: stackName, App: appName}):
		return stackName
	}
	return BaseStack
}

// FixedPorts gives the fixed ports of an instance. Only the base stack has them, so two stacks never ask for one port.
func (snapshot Snapshot) FixedPorts(instance Instance) map[string]int {
	if instance.Stack == BaseStack {
		return snapshot.App(instance).FixedPorts
	}
	return nil
}

// StartOrder gives the instances to start, each one after its dependencies, and the external services that they need.
// A dependency that the request names, or one with the scope stack, comes from the stack of its dependent.
func (snapshot Snapshot) StartOrder(requested []Instance, skipDependencies bool) (order []Instance, externalNames []string) {
	isPlanned := map[Instance]bool{}
	isExternalDependency := map[string]bool{}
	var visit func(instance Instance)
	visit = func(instance Instance) {
		if isPlanned[instance] {
			return
		}
		isPlanned[instance] = true
		if !skipDependencies {
			for _, dependency := range snapshot.App(instance).DependsOn {
				if _, isExternal := snapshot.External(dependency); isExternal {
					isExternalDependency[dependency] = true
					continue
				}
				dependencyStack := snapshot.Serving(instance.Stack, dependency)
				if slices.Contains(requested, Instance{Stack: instance.Stack, App: dependency}) || snapshot.configOf(snapshot.Own).Apps[dependency].Scope == config.ScopeStack {
					dependencyStack = instance.Stack
				}
				visit(Instance{Stack: dependencyStack, App: dependency})
			}
		}
		order = append(order, instance)
	}
	for _, instance := range requested {
		visit(instance)
	}
	return order, slices.Sorted(maps.Keys(isExternalDependency))
}

// PortNeed is a port that an instance has no number for yet.
type PortNeed struct {
	Instance Instance
	PortName string
}

// Start is what ilot start does: the order of the instances, the external services to test, and the ports to give.
type Start struct {
	Order     []Instance
	Externals []string
	// The fixed ports of the base stack. The engine tests that no other program holds one before it starts an instance that does not run.
	FixedPorts []FixedPort
	// The ports to give a number, in order.
	PortNeeds []PortNeed
	// The snapshot after the start: every instance of the order is a member, with its fixed ports.
	After Snapshot
}

type FixedPort struct {
	Instance Instance
	PortName string
	Port     int
}

// PlanStart makes every instance of the order a member before any template renders, so a template sees the stack as it is after the start.
func (snapshot Snapshot) PlanStart(requested []Instance, skipDependencies bool) Start {
	order, externalNames := snapshot.StartOrder(requested, skipDependencies)
	after := snapshot.Clone()
	start := Start{Order: order, Externals: externalNames, FixedPorts: nil, PortNeeds: nil, After: after}
	for _, instance := range order {
		ports := after.Ports(instance)
		for _, portName := range slices.Sorted(maps.Keys(after.FixedPorts(instance))) {
			fixedPort := after.FixedPorts(instance)[portName]
			start.FixedPorts = append(start.FixedPorts, FixedPort{Instance: instance, PortName: portName, Port: fixedPort})
			after.SetPort(instance, portName, fixedPort)
		}
		for _, portName := range after.App(instance).PortNames() {
			if ports[portName] == 0 && after.FixedPorts(instance)[portName] == 0 {
				start.PortNeeds = append(start.PortNeeds, PortNeed{Instance: instance, PortName: portName})
			}
		}
		after.SetWanted(instance, true)
	}
	return start
}

// DropUndeclared takes the membership away from each instance that no loaded configuration declares any more, and gives them.
func (snapshot Snapshot) DropUndeclared() []Instance {
	var dropped []Instance
	for _, wantedInstance := range snapshot.Wanted() {
		if _, isDeclared := snapshot.Declaration(wantedInstance); !isDeclared {
			snapshot.SetWanted(wantedInstance, false)
			dropped = append(dropped, wantedInstance)
		}
	}
	return dropped
}
