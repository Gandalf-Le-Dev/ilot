package engine

import (
	"fmt"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

// Source is where a command reads its snapshot: the state, the files of the checkouts and the login shell.
type Source struct {
	// The name of the workspace, which is also the name of its root repo.
	Workspace string
	// The main checkout of the root repo, which the state records for the workspace.
	Root string
	// The configuration of the stack of the command, or why it cannot be read. ilot stop, ilot rm, ilot ls and ilot logs work without it.
	Config            config.Config
	ConfigErr         error
	Store             state.Store
	CheckoutsForStack func(stackName string) (map[string]workspace.Worktree, error)
	ConfigForStack    func(stackName string) (config.Config, error)
	ReposNotCloned    map[string]string
	Settings          config.WorkspaceSettings
	// The machine, with no shell environment: ShellEnv reads it when a command renders.
	Machine plan.Machine
	// ShellEnv gives the environment of a login shell, and apart from it the variables of the terminal that no shell file gives.
	ShellEnv func() (shellEnv, sessionEnv map[string]string, err error)
}

// StackDir holds the logs and the binaries of a stack.
func (source Source) StackDir(stackName string) string {
	return source.Store.StackDir(source.Workspace, stackName)
}

// snapshotOf gives the snapshot of a state, with no checkout and no shell environment.
// A command reads the configuration of its own stack and of base only, so a broken ilot.yaml of another stack does not stop it.
func (source Source) snapshotOf(current state.State, ownStackName string) (plan.Snapshot, error) {
	if source.ConfigErr != nil {
		return plan.Snapshot{}, source.ConfigErr
	}
	configByStack := map[string]config.Config{ownStackName: source.Config}
	if ownStackName != plan.BaseStack && source.ConfigForStack != nil {
		baseConfig, err := source.ConfigForStack(plan.BaseStack)
		if err != nil {
			return plan.Snapshot{}, fmt.Errorf("read the config of the %s stack, which serves the apps that this stack does not run: %w", plan.BaseStack, err)
		}
		configByStack[plan.BaseStack] = baseConfig
	}
	return plan.Snapshot{
		Workspace:      source.Workspace,
		Own:            ownStackName,
		State:          current,
		Configs:        configByStack,
		Checkouts:      map[string]map[string]string{},
		ReposNotCloned: source.ReposNotCloned,
		Settings:       source.Settings,
		Machine:        source.Machine,
	}, nil
}

// Snapshot gives what a command of a stack renders from. The instances that no config declares any more lose their membership in it.
func (source Source) Snapshot(stackName string) (plan.Snapshot, []plan.Instance, error) {
	machine, err := source.machineWithShell()
	if err != nil {
		return plan.Snapshot{}, nil, err
	}
	return source.snapshotOn(machine, stackName)
}

// machineWithShell gives the machine with the environment of the login shell.
// A login shell can take seconds, so a command reads it before it takes the state lock.
func (source Source) machineWithShell() (plan.Machine, error) {
	machine := source.Machine
	var err error
	machine.ShellEnv, machine.SessionEnv, err = source.shellEnvironment()
	return machine, err
}

// snapshotOn is Snapshot on a machine whose shell environment is already read.
func (source Source) snapshotOn(machine plan.Machine, stackName string) (plan.Snapshot, []plan.Instance, error) {
	current, err := source.Store.Load()
	if err != nil {
		return plan.Snapshot{}, nil, err
	}
	current.Stack(source.Workspace, plan.BaseStack)
	current.Stack(source.Workspace, stackName)
	snapshot, err := source.snapshotOf(current, stackName)
	if err != nil {
		return plan.Snapshot{}, nil, err
	}
	snapshot.Machine = machine
	if err := source.deriveCheckouts(snapshot, stackName); err != nil {
		return plan.Snapshot{}, nil, err
	}
	return snapshot, snapshot.DropUndeclared(), nil
}

// addStack loads the configuration and the checkouts of one more stack into a snapshot.
func (source Source) addStack(snapshot plan.Snapshot, stackName string) error {
	if _, isLoaded := snapshot.Configs[stackName]; isLoaded {
		return nil
	}
	if source.ConfigForStack == nil {
		snapshot.Configs[stackName] = source.Config
		return source.deriveCheckouts(snapshot, stackName)
	}
	stackConfig, err := source.ConfigForStack(stackName)
	if err != nil {
		return fmt.Errorf("read the config of stack %s: %w: fix it, or remove the stack with ilot rm %s", stackName, err, stackName)
	}
	snapshot.Configs[stackName] = stackConfig
	return source.deriveCheckouts(snapshot, stackName)
}

// A bound worktree that is deleted gives its repo back to the main checkout.
func (source Source) deriveCheckouts(snapshot plan.Snapshot, ownStackName string) error {
	for _, stackName := range []string{plan.BaseStack, ownStackName} {
		checkoutByRepo, err := source.CheckoutsForStack(stackName)
		if err != nil {
			return err
		}
		snapshot.Checkouts[stackName] = map[string]string{}
		for repoName, checkout := range checkoutByRepo {
			snapshot.Checkouts[stackName][repoName] = checkout.Dir
		}
	}
	return nil
}

func (source Source) shellEnvironment() (shellEnv, sessionEnv map[string]string, err error) {
	if source.ShellEnv == nil {
		return map[string]string{}, map[string]string{}, nil
	}
	return source.ShellEnv()
}

// Environment gives the environment of an app in a stack, with where each value comes from.
func (source Source) Environment(stackName, appName string) ([]plan.EnvEntry, error) {
	snapshot, _, err := source.Snapshot(stackName)
	if err != nil {
		return nil, err
	}
	appInstances, err := snapshot.Instances(stackName, []string{appName})
	if err != nil {
		return nil, err
	}
	return snapshot.Environment(appInstances[0])
}

// LogFile gives the log of an app in a stack. An app with the scope base has one log, in the base stack.
func (source Source) LogFile(stackName, appName string) (string, error) {
	current, err := source.Store.Load()
	if err != nil {
		return "", err
	}
	appInstances, err := source.instancesToStop(current, stackName, []string{appName})
	if err != nil {
		return "", err
	}
	return plan.Snapshot{Workspace: source.Workspace, Machine: source.Machine}.LogFile(appInstances[0]), nil
}

// instancesToStop names the instances of a command that only reads or stops. With a configuration, the scope of each app decides.
// Without one, or for an app that the configuration no longer declares, the state decides:
// the own stack if it has the app, else the base stack, which holds every app with the scope base.
func (source Source) instancesToStop(current state.State, stackName string, appNames []string) ([]plan.Instance, error) {
	snapshot := plan.Snapshot{Workspace: source.Workspace, Own: stackName, State: current, Configs: map[string]config.Config{stackName: source.Config}, ReposNotCloned: source.ReposNotCloned}
	var instances []plan.Instance
	for _, appName := range appNames {
		if _, isDeclared := source.Config.Apps[appName]; isDeclared && source.ConfigErr == nil {
			instances = append(instances, plan.Instance{Stack: snapshot.StackOf(stackName, appName), App: appName})
			continue
		}
		stackOfApp := stackName
		if !current.HasApp(source.Workspace, stackName, appName) {
			stackOfApp = plan.BaseStack
		}
		if current.HasApp(source.Workspace, stackOfApp, appName) {
			instances = append(instances, plan.Instance{Stack: stackOfApp, App: appName})
			continue
		}
		if source.ConfigErr != nil {
			return nil, fmt.Errorf("%w; and no stack has the app %q", source.ConfigErr, appName)
		}
		_, err := snapshot.Instances(stackName, []string{appName})
		return nil, err
	}
	return instances, nil
}
