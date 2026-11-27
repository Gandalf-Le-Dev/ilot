package ilotctl

import (
	"cmp"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/appconfig"
	"github.com/Gandalf-Le-Dev/ilot/internal/personal"
	"github.com/Gandalf-Le-Dev/ilot/internal/stack"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
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

func CheckRendering(config appconfig.Config) []error {
	controller := Controller{
		Config:              config,
		Store:               state.Store{RootDir: madeUpCheckoutsRoot + "state"},
		Supervisor:          nil,
		RunBuild:            nil,
		EnsureImage:         nil,
		IsPortFreeOnHost:    nil,
		IsExternalServiceUp: nil,
		CheckoutsForStack:   nil,
		ConfigForStack:      nil,
		ReposNotCloned:      nil,
		Personal:            personal.Settings{Env: nil, Apps: nil, Repos: nil},
		ShellEnv:            nil,
		Login:               checkStackName,
		BackendHost:         "",
		ProxyPort:           "",
		ReadyTimeout:        0,
		ProgressInterval:    0,
		Output:              io.Discard,
		configByStack:       nil,
	}

	stacks := stacksWithEveryAppRunning(config)
	var problems []error
	for _, appName := range slices.Sorted(maps.Keys(config.Apps)) {
		appInstance := instance{stackName: controller.stackOf(checkStackName, appName), appName: appName}
		repoName := cmp.Or(config.Apps[appName].Repo, appconfig.DefaultRepo)
		for _, err := range controller.renderEveryTemplateOf(stacks, appInstance) {
			problems = append(problems, RenderProblem{App: appName, Repo: repoName, Err: err})
		}
	}
	return problems
}

func stacksWithEveryAppRunning(config appconfig.Config) stackSet {
	repoNames := []string{appconfig.DefaultRepo}
	for repoName := range config.Repos {
		repoNames = append(repoNames, repoName)
	}
	for _, app := range config.Apps {
		if app.Repo != "" {
			repoNames = append(repoNames, app.Repo)
		}
	}
	stacks := stackSet{byName: map[string]*state.StackState{}}
	for _, stackName := range []string{stack.MasterStackName, stack.SharedStackName, checkStackName} {
		stackState := stacks.ensure(stackName)
		for _, repoName := range repoNames {
			stackState.CheckoutDirs[repoName] = madeUpCheckoutsRoot + repoName
		}
	}
	nextMadeUpPort := firstMadeUpPort
	for _, appName := range slices.Sorted(maps.Keys(config.Apps)) {
		app := config.Apps[appName]
		stackNames := []string{checkStackName, stack.MasterStackName}
		if app.Shared {
			stackNames = []string{stack.SharedStackName}
		}
		for _, stackName := range stackNames {
			portByName := map[string]int{}
			for _, portName := range app.Ports {
				if fixedPort, isFixed := app.FixedPorts[portName]; isFixed {
					portByName[portName] = fixedPort
					continue
				}
				portByName[portName] = nextMadeUpPort
				nextMadeUpPort++
			}
			stacks.setApp(instance{stackName: stackName, appName: appName}, state.AppState{Ports: portByName, Running: true})
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
	repoEnv := controller.repoEnvOf(appInstance)[cmp.Or(app.Repo, appconfig.DefaultRepo)]
	for _, valueByKey := range []map[string]string{repoEnv, app.Env} {
		for _, key := range slices.Sorted(maps.Keys(valueByKey)) {
			if strings.Contains(valueByKey[key], "{{") {
				templates = append(templates, valueByKey[key])
			}
		}
	}
	for _, containerPath := range slices.Sorted(maps.Keys(app.Container.Mounts)) {
		templates = append(templates, app.Container.Mounts[containerPath])
	}
	var problems []error
	for _, templateText := range templates {
		if _, err := controller.renderTemplate(stacks, appInstance, templateText); err != nil {
			problems = append(problems, err)
		}
	}
	return problems
}
