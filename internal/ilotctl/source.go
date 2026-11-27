package ilotctl

import (
	"errors"
	"fmt"
	"maps"
	"slices"

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
	// The configuration of the stack of the command.
	Config            config.Config
	Store             state.Store
	CheckoutsForStack func(stackName string) (map[string]workspace.Worktree, error)
	ConfigForStack    func(stackName string) (config.Config, error)
	ReposNotCloned    map[string]string
	Settings          config.WorkspaceSettings
	// The machine, with no shell environment: ShellEnv reads it when a command renders.
	Machine  plan.Machine
	ShellEnv func() (map[string]string, error)
}

// StackDir holds the logs and the binaries of a stack.
func (source Source) StackDir(stackName string) string {
	return source.Store.StackDir(source.Workspace, stackName)
}

// A stack whose config cannot be read keeps the config of the command, so a listing still shows it.
func (source Source) loadStackConfigs(stackNames []string) (map[string]config.Config, []error) {
	configByStack := map[string]config.Config{}
	if source.ConfigForStack == nil {
		return configByStack, nil
	}
	var problems []error
	for _, stackName := range stackNames {
		stackConfig, err := source.ConfigForStack(stackName)
		if err != nil {
			problems = append(problems, fmt.Errorf("read the config of stack %s: %w: fix it, or remove the stack with ilot rm %s", stackName, err, stackName))
			continue
		}
		configByStack[stackName] = stackConfig
	}
	return configByStack, problems
}

// snapshotOf gives the snapshot of a state, with no checkout and no shell environment. The problems are the stacks whose config cannot be read.
func (source Source) snapshotOf(current state.State, ownStackName string) (plan.Snapshot, []error) {
	stackNames := slices.Sorted(maps.Keys(current.Stacks(source.Workspace)))
	configByStack, problems := source.loadStackConfigs(stackNames)
	configByStack[ownStackName] = source.Config
	return plan.Snapshot{
		Workspace:      source.Workspace,
		Own:            ownStackName,
		State:          current,
		Configs:        configByStack,
		Checkouts:      map[string]map[string]string{},
		ReposNotCloned: source.ReposNotCloned,
		Settings:       source.Settings,
		Machine:        source.Machine,
	}, problems
}

// Snapshot gives what a command of a stack renders from. The instances that no config declares any more lose their membership in it.
func (source Source) Snapshot(stackName string) (plan.Snapshot, []plan.Instance, error) {
	current, err := source.Store.Load()
	if err != nil {
		return plan.Snapshot{}, nil, err
	}
	current.Stack(source.Workspace, plan.BaseStack)
	current.Stack(source.Workspace, stackName)
	return source.complete(current, stackName)
}

func (source Source) complete(current state.State, stackName string) (plan.Snapshot, []plan.Instance, error) {
	snapshot, problems := source.snapshotOf(current, stackName)
	if err := errors.Join(problems...); err != nil {
		return plan.Snapshot{}, nil, err
	}
	if err := source.deriveCheckouts(snapshot); err != nil {
		return plan.Snapshot{}, nil, err
	}
	shellEnv, err := source.shellEnvironment()
	if err != nil {
		return plan.Snapshot{}, nil, err
	}
	snapshot.Machine.ShellEnv = shellEnv
	return snapshot, snapshot.DropUndeclared(), nil
}

// A bound worktree that is deleted gives its repo back to the main checkout.
func (source Source) deriveCheckouts(snapshot plan.Snapshot) error {
	for stackName := range snapshot.State.Stacks(source.Workspace) {
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

func (source Source) shellEnvironment() (map[string]string, error) {
	if source.ShellEnv == nil {
		return map[string]string{}, nil
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
	snapshot, _ := source.snapshotOf(state.New(), stackName)
	appInstances, err := snapshot.Instances(stackName, []string{appName})
	if err != nil {
		return "", err
	}
	return snapshot.LogFile(appInstances[0]), nil
}
