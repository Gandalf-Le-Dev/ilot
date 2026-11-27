package ilotctl

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
)

type EnvEntry struct {
	Key    string
	Value  string
	Source string
}

const (
	sourceShell     = "shell"
	sourceRoot      = "root repo"
	sourceRepo      = "repo"
	sourceApp       = "app"
	sourceGlobal    = "global"
	sourceGlobalApp = "global app"
	sourceComputed  = "computed"
)

func (controller *Controller) Environment(stackName string, appName string) ([]EnvEntry, error) {
	snapshot, err := controller.loadStacks(stackName)
	if err != nil {
		return nil, err
	}
	appInstances, err := snapshot.Instances(stackName, []string{appName})
	if err != nil {
		return nil, err
	}
	return controller.environmentOf(snapshot, appInstances[0])
}

func (controller *Controller) shellEnvironment() (map[string]string, error) {
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
	return environ(shellEnv), nil
}

// The layers, and the last one wins: the login shell, the env of the root repo, the env of the repo of the app, the env of the app, the global file.
// A value written with a template is computed by ilot (ports, addresses, the stack), so a value of the global file cannot override it.
func (controller *Controller) environmentOf(snapshot plan.Snapshot, appInstance plan.Instance) ([]EnvEntry, error) {
	app := snapshot.App(appInstance)
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
	literalRootEnv, templatedRootEnv := splitByTemplate(repoEnvOf(snapshot, appInstance)[controller.Workspace])
	literalRepoEnv, templatedRepoEnv := splitByTemplate(repoEnvOf(snapshot, appInstance)[cmp.Or(app.Repo, controller.Workspace)])
	literalAppEnv, templatedAppEnv := splitByTemplate(app.Env)
	setAll(sourceRoot, literalRootEnv)
	setAll(sourceRepo, literalRepoEnv)
	setAll(sourceApp, literalAppEnv)
	setAll(sourceGlobal, controller.Settings.Env)
	setAll(sourceGlobalApp, controller.Settings.Apps[appInstance.App])
	for _, templatedEnv := range []map[string]string{templatedRootEnv, templatedRepoEnv, templatedAppEnv} {
		for _, key := range slices.Sorted(maps.Keys(templatedEnv)) {
			value, err := controller.renderTemplate(snapshot, appInstance, templatedEnv[key])
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
