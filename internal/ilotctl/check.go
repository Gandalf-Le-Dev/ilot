package ilotctl

import (
	"cmp"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/ports"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

const (
	checkStackName      = "check"
	firstMadeUpPort     = 20000
	madeUpCheckoutsRoot = "/check/"
)

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
	controller := Controller{
		Workspace:           workspaceName,
		Config:              merged,
		Store:               StackStore{Files: state.Store{RootDir: madeUpCheckoutsRoot + "state"}, Workspace: workspaceName, Root: ""},
		Supervisor:          nil,
		RunBuild:            nil,
		EnsureImage:         nil,
		IsPortFreeOnHost:    nil,
		IsExternalServiceUp: nil,
		CheckoutsForStack:   nil,
		ConfigForStack:      nil,
		ReposNotCloned:      nil,
		Settings:            config.WorkspaceSettings{Repos: nil, Env: nil, Apps: nil},
		Domain:              config.DefaultDomain,
		PortRange:           ports.Range{First: 0, Last: 0},
		ShellEnv:            nil,
		Login:               checkStackName,
		ProxyPort:           "",
		IlotBinary:          "ilot",
		IlotEnv:             nil,
		ReadyTimeout:        0,
		ProgressInterval:    0,
		Output:              io.Discard,
		configByStack:       nil,
	}

	stacks := stacksWithEveryAppRunning(append([]string{workspaceName}, repoNames...), merged)
	var problems []error
	for _, appName := range slices.Sorted(maps.Keys(merged.Apps)) {
		appInstance := instance{stackName: controller.stackOf(checkStackName, appName), appName: appName}
		repoName := cmp.Or(merged.Apps[appName].Repo, workspaceName)
		for _, err := range controller.renderEveryTemplateOf(stacks, appInstance) {
			problems = append(problems, RenderProblem{App: appName, Repo: repoName, Err: err})
		}
	}
	return problems
}

func stacksWithEveryAppRunning(repoNames []string, merged config.Config) stackSet {
	for _, app := range merged.Apps {
		if app.Repo != "" {
			repoNames = append(repoNames, app.Repo)
		}
	}
	stacks := stackSet{byName: map[string]*StackState{}}
	for _, stackName := range []string{workspace.BaseStackName, checkStackName} {
		stackState := stacks.ensure(stackName)
		for _, repoName := range repoNames {
			stackState.CheckoutDirs[repoName] = madeUpCheckoutsRoot + repoName
		}
	}
	nextMadeUpPort := firstMadeUpPort
	for _, appName := range slices.Sorted(maps.Keys(merged.Apps)) {
		app := merged.Apps[appName]
		stackNames := []string{checkStackName, workspace.BaseStackName}
		if app.Scope == config.ScopeBase {
			stackNames = []string{workspace.BaseStackName}
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
			stacks.setApp(instance{stackName: stackName, appName: appName}, AppState{Ports: portByName, Running: true})
		}
	}
	return stacks
}

// Each template renders on its own, so one broken value does not hide the next one.
func (controller *Controller) renderEveryTemplateOf(stacks stackSet, appInstance instance) []error {
	app := controller.app(appInstance)
	var templates []string
	if app.Build != "" {
		templates = append(templates, app.Build)
	}
	if !controller.isContainer(appInstance) {
		templates = append(templates, app.Run)
	}
	if app.Stop != "" {
		templates = append(templates, app.Stop)
	}
	repoEnv := controller.repoEnvOf(appInstance)[cmp.Or(app.Repo, controller.Workspace)]
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
		if _, err := controller.renderTemplate(stacks, appInstance, templateText); err != nil {
			problems = append(problems, err)
		}
	}
	return problems
}
