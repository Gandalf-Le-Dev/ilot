package plan

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

const (
	checkStackName      = "check"
	firstMadeUpPort     = 20000
	madeUpCheckoutsRoot = "/check/"
)

// RenderProblem is a template of an app that does not render, with the repo whose file declares the app.
type RenderProblem struct {
	App  string
	Repo string
	Err  error
}

func (problem RenderProblem) Error() string {
	return problem.App + ": " + problem.Err.Error()
}

func (problem RenderProblem) Unwrap() error {
	return problem.Err
}

// CheckRendering renders every template of every app, with made-up ports and a made-up folder for each repo.
func CheckRendering(workspaceName string, repoNames []string, merged config.Config) []error {
	snapshot := snapshotWithEveryAppRunning(workspaceName, append([]string{workspaceName}, repoNames...), merged)
	var problems []error
	for _, appName := range slices.Sorted(maps.Keys(merged.Apps)) {
		appInstance := Instance{Stack: snapshot.StackOf(checkStackName, appName), App: appName}
		repoName := cmp.Or(merged.Apps[appName].Repo, workspaceName)
		for _, err := range snapshot.renderEveryTemplateOf(appInstance) {
			problems = append(problems, RenderProblem{App: appName, Repo: repoName, Err: err})
		}
	}
	return problems
}

func snapshotWithEveryAppRunning(workspaceName string, repoNames []string, merged config.Config) Snapshot {
	for _, app := range merged.Apps {
		if app.Repo != "" {
			repoNames = append(repoNames, app.Repo)
		}
	}
	snapshot := Snapshot{
		Workspace:      workspaceName,
		Own:            checkStackName,
		State:          state.New(),
		Configs:        map[string]config.Config{checkStackName: merged},
		Checkouts:      map[string]map[string]string{},
		ReposNotCloned: nil,
		Settings:       config.WorkspaceSettings{Repos: nil, Env: nil, Apps: nil},
		Machine:        Machine{StateDir: madeUpCheckoutsRoot + "state", Domain: config.DefaultDomain, ProxyPort: "", Login: checkStackName, ShellEnv: nil, IlotBinary: "ilot", IlotEnv: nil},
	}
	for _, stackName := range []string{BaseStack, checkStackName} {
		snapshot.Checkouts[stackName] = map[string]string{}
		for _, repoName := range repoNames {
			snapshot.Checkouts[stackName][repoName] = madeUpCheckoutsRoot + repoName
		}
	}
	nextMadeUpPort := firstMadeUpPort
	for _, appName := range slices.Sorted(maps.Keys(merged.Apps)) {
		app := merged.Apps[appName]
		stackNames := []string{checkStackName, BaseStack}
		if app.Scope == config.ScopeBase {
			stackNames = []string{BaseStack}
		}
		for _, stackName := range stackNames {
			portByName := map[string]int{}
			for _, portName := range app.PortNames() {
				if fixedPort, isFixed := app.FixedPorts[portName]; isFixed {
					portByName[portName] = fixedPort
					continue
				}
				portByName[portName] = nextMadeUpPort
				nextMadeUpPort++
			}
			snapshot.State.Stack(workspaceName, stackName).Apps[appName] = state.App{Wanted: true, Ports: portByName, Route: nil, Container: ""}
		}
	}
	return snapshot
}

// Each template renders on its own, so one broken value does not hide the next one.
func (snapshot Snapshot) renderEveryTemplateOf(appInstance Instance) []error {
	app := snapshot.App(appInstance)
	var templates []string
	if app.Build != "" {
		templates = append(templates, app.Build)
	}
	if !snapshot.IsContainer(appInstance) {
		templates = append(templates, app.Run)
	}
	if app.Stop != "" {
		templates = append(templates, app.Stop)
	}
	repoEnv := snapshot.repoEnvOf(appInstance)[cmp.Or(app.Repo, snapshot.Workspace)]
	for _, valueByKey := range []map[string]string{repoEnv, app.Env} {
		for _, key := range slices.Sorted(maps.Keys(valueByKey)) {
			if strings.Contains(valueByKey[key], "{{") {
				templates = append(templates, valueByKey[key])
			}
		}
	}
	templates = append(templates, slices.Sorted(maps.Keys(app.Container.Mounts))...)
	templates = append(templates, app.Container.Args...)
	var problems []error
	for _, templateText := range templates {
		if _, err := snapshot.Render(appInstance, templateText); err != nil {
			problems = append(problems, err)
		}
	}
	return problems
}
