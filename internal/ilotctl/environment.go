package ilotctl

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/appconfig"
	"github.com/Gandalf-Le-Dev/ilot/internal/processcompose"
	"github.com/Gandalf-Le-Dev/ilot/internal/stack"
)

type EnvEntry struct {
	Key    string
	Value  string
	Source string
}

const (
	sourceShell       = "shell"
	sourceRepo        = "repo"
	sourceApp         = "app"
	sourcePersonal    = "personal"
	sourcePersonalApp = "personal app"
	sourceComputed    = "computed"
)

func (controller *Controller) Environment(worktree stack.Worktree, appName string) ([]EnvEntry, error) {
	stacks, err := controller.loadStacks(worktree)
	if err != nil {
		return nil, err
	}
	appInstances, err := controller.instancesOf(worktree.Name, []string{appName})
	if err != nil {
		return nil, err
	}
	return controller.environmentOf(stacks, appInstances[0])
}

// A daemon started before ilot recorded its environment gives the apps the environment of the shell that started it.
func (controller *Controller) RunsWithoutARecordedEnvironment(ctx context.Context) (bool, error) {
	if !controller.Supervisor.IsUp(ctx) {
		return false, nil
	}
	_, isRecorded, err := controller.Supervisor.RecordedEnvironment()
	return !isRecorded, err
}

// The environment of the running daemon is what the apps get; the environment of the shell files is what a new daemon gets.
func (controller *Controller) shellEnvironment() (map[string]string, error) {
	if controller.isShellEnvResolved {
		return controller.resolvedShellEnv, nil
	}
	resolved, err := controller.resolveShellEnvironment()
	if err != nil {
		return nil, err
	}
	controller.resolvedShellEnv = resolved
	controller.isShellEnvResolved = true
	return resolved, nil
}

func (controller *Controller) resolveShellEnvironment() (map[string]string, error) {
	if controller.Supervisor != nil {
		recorded, isRecorded, err := controller.Supervisor.RecordedEnvironment()
		if err != nil || isRecorded {
			return recorded, err
		}
	}
	if controller.ShellEnv == nil {
		return map[string]string{}, nil
	}
	return controller.ShellEnv()
}

func (controller *Controller) shellEnviron() ([]string, error) {
	shellEnv, err := controller.shellEnvironment()
	if err != nil {
		return nil, err
	}
	return processcompose.Environ(shellEnv), nil
}

// A value written with a template is computed by ilot (ports, addresses, the stack), so a personal value cannot override it.
func (controller *Controller) environmentOf(stacks stackSet, appInstance instance) ([]EnvEntry, error) {
	app := controller.app(appInstance)
	shellEnv, err := controller.shellEnvironment()
	if err != nil {
		return nil, err
	}
	entryByKey := map[string]EnvEntry{}
	setAll := func(source string, valueByKey map[string]string) {
		for key, value := range valueByKey {
			entryByKey[key] = EnvEntry{Key: key, Value: value, Source: source}
		}
	}
	setAll(sourceShell, shellEnv)
	literalRepoEnv, templatedRepoEnv := splitByTemplate(controller.repoEnvOf(appInstance)[cmp.Or(app.Repo, appconfig.DefaultRepo)])
	literalAppEnv, templatedAppEnv := splitByTemplate(app.Env)
	setAll(sourceRepo, literalRepoEnv)
	setAll(sourceApp, literalAppEnv)
	setAll(sourcePersonal, controller.Personal.Env)
	setAll(sourcePersonalApp, controller.Personal.Apps[appInstance.appName])
	for _, templatedEnv := range []map[string]string{templatedRepoEnv, templatedAppEnv} {
		for _, key := range slices.Sorted(maps.Keys(templatedEnv)) {
			value, err := controller.renderTemplate(stacks, appInstance, templatedEnv[key])
			if err != nil {
				return nil, err
			}
			entryByKey[key] = EnvEntry{Key: key, Value: value, Source: sourceComputed}
		}
	}
	var entries []EnvEntry
	for _, key := range slices.Sorted(maps.Keys(entryByKey)) {
		entries = append(entries, entryByKey[key])
	}
	return entries, nil
}

func splitByTemplate(valueByKey map[string]string) (literalValues, templatedValues map[string]string) {
	literalValues, templatedValues = map[string]string{}, map[string]string{}
	for key, value := range valueByKey {
		if strings.Contains(value, "{{") {
			templatedValues[key] = value
			continue
		}
		literalValues[key] = value
	}
	return literalValues, templatedValues
}
