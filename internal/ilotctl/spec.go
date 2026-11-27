package ilotctl

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/Gandalf-Le-Dev/ilot/internal/appconfig"
	"github.com/Gandalf-Le-Dev/ilot/internal/processcompose"
	"github.com/Gandalf-Le-Dev/ilot/internal/stack"
)

func (controller *Controller) binDir(stackName string) string {
	return filepath.Join(controller.Store.StackDir(stackName), "bin")
}

func (controller *Controller) LogFile(worktree stack.Worktree, appName string) (string, error) {
	appInstances, err := controller.instancesOf(worktree.Name, []string{appName})
	if err != nil {
		return "", err
	}
	return controller.logFile(appInstances[0]), nil
}

func (controller *Controller) logFile(appInstance instance) string {
	return filepath.Join(controller.Store.StackDir(appInstance.stackName), "logs", appInstance.appName+".log")
}

func (controller *Controller) processSpec(stacks stackSet, appInstance instance) (processcompose.ProcessSpec, error) {
	envEntries, err := controller.environmentOf(stacks, appInstance)
	if err != nil {
		return processcompose.ProcessSpec{}, err
	}
	env := map[string]string{}
	var command string
	if controller.isContainer(appInstance) {
		envEntries = containerEnvEntries(envEntries)
		command, err = controller.containerCommand(stacks, appInstance, envEntries)
	} else {
		command, err = controller.processCommand(stacks, appInstance)
	}
	for _, entry := range envEntries {
		if entry.Source != sourceShell {
			env[entry.Key] = entry.Value
		}
	}
	if err != nil {
		return processcompose.ProcessSpec{}, err
	}
	if err := os.MkdirAll(filepath.Dir(controller.logFile(appInstance)), 0o700); err != nil {
		return processcompose.ProcessSpec{}, err
	}
	workingDir, err := controller.workingDir(stacks, appInstance)
	if err != nil {
		return processcompose.ProcessSpec{}, err
	}
	shutdownCommand, err := controller.shutdownCommand(stacks, appInstance)
	if err != nil {
		return processcompose.ProcessSpec{}, err
	}
	return processcompose.ProcessSpec{
		Name:            appInstance.processName(),
		Command:         command,
		WorkingDir:      workingDir,
		Env:             env,
		LogFile:         controller.logFile(appInstance),
		Ready:           controller.readyProbe(stacks, appInstance),
		ShutdownCommand: shutdownCommand,
	}, nil
}

func (controller *Controller) processCommand(stacks stackSet, appInstance instance) (string, error) {
	command, err := controller.renderTemplate(stacks, appInstance, controller.app(appInstance).Run)
	if err != nil {
		return "", err
	}
	checkoutDir, err := controller.checkoutDir(stacks, appInstance)
	if err != nil {
		return "", err
	}
	return controller.withRepoTools(checkoutDir, command), nil
}

func (controller *Controller) shutdownCommand(stacks stackSet, appInstance instance) (string, error) {
	if controller.isContainer(appInstance) {
		return containerStopCommand(appInstance), nil
	}
	stop := controller.app(appInstance).Stop
	if stop == "" {
		return "", nil
	}
	command, err := controller.renderTemplate(stacks, appInstance, stop)
	if err != nil {
		return "", err
	}
	checkoutDir, err := controller.checkoutDir(stacks, appInstance)
	if err != nil {
		return "", err
	}
	return controller.withRepoTools(checkoutDir, command), nil
}

func (controller *Controller) withRepoTools(checkoutDir, shellCommand string) string {
	if !controller.UsesMise(checkoutDir) {
		return shellCommand
	}
	return "mise exec -- bash -c " + processcompose.ShellQuote(shellCommand)
}

func (controller *Controller) workingDir(stacks stackSet, appInstance instance) (string, error) {
	appDir := controller.app(appInstance).Dir
	if filepath.IsAbs(appDir) {
		return appDir, nil
	}
	checkoutDir, err := controller.checkoutDir(stacks, appInstance)
	if err != nil {
		return "", err
	}
	return filepath.Join(checkoutDir, appDir), nil
}

func (controller *Controller) watcherSpec(stacks stackSet, watchedInstance instance, workingDir string) (processcompose.ProcessSpec, bool, error) {
	app := controller.app(watchedInstance)
	watch := app.Watch
	if len(watch.Paths) == 0 {
		return processcompose.ProcessSpec{}, false, nil
	}
	onChange, err := controller.watcherCommand(stacks, watchedInstance, app)
	if err != nil {
		return processcompose.ProcessSpec{}, false, err
	}
	commandParts := []string{"watchexec --postpone"}
	for _, watchedPath := range watch.Paths {
		commandParts = append(commandParts, "--watch "+processcompose.ShellQuote(filepath.Join(workingDir, watchedPath)))
	}
	if len(watch.Exts) > 0 {
		commandParts = append(commandParts, "--exts "+strings.Join(watch.Exts, ","))
	}
	commandParts = append(commandParts, "--", processcompose.ShellQuote(onChange))
	watcherInstance := instance{stackName: watchedInstance.stackName, appName: watchedInstance.appName + "-watch"}
	return processcompose.ProcessSpec{
		Name:            watcherInstance.processName(),
		Command:         strings.Join(commandParts, " "),
		WorkingDir:      workingDir,
		Env:             nil,
		LogFile:         controller.logFile(watcherInstance),
		Ready:           processcompose.ReadyProbe{Host: "", Port: 0, Path: "", Command: ""},
		ShutdownCommand: "",
	}, true, nil
}

// An app with a build rebuilds before the restart, in its checkout, like ilot start does.
// A failed build keeps the running app. The lock serializes the builds of the stack:
// apps that share a binary, such as internalapi and publicapi, would otherwise write it at the same time.
func (controller *Controller) watcherCommand(stacks stackSet, watchedInstance instance, app appconfig.App) (string, error) {
	restart := controller.Supervisor.RestartCommand(watchedInstance.processName())
	if app.Build == "" {
		return restart, nil
	}
	buildCommand, err := controller.renderTemplate(stacks, watchedInstance, app.Build)
	if err != nil {
		return "", err
	}
	checkoutDir, err := controller.checkoutDir(stacks, watchedInstance)
	if err != nil {
		return "", err
	}
	lockFile := filepath.Join(controller.Store.StackDir(watchedInstance.stackName), "build.lock")
	build := "cd " + processcompose.ShellQuote(checkoutDir) + " && " + controller.withRepoTools(checkoutDir, buildCommand)
	return "flock " + processcompose.ShellQuote(lockFile) + " bash -c " + processcompose.ShellQuote(build) + " && " + restart, nil
}

func (controller *Controller) binDirIsEmpty(stackName string) bool {
	entries, err := os.ReadDir(controller.binDir(stackName))
	return err != nil || len(entries) == 0
}

func readyPortName(app appconfig.App) string {
	switch {
	case app.Ready.Port != "":
		return app.Ready.Port
	case app.Route != "":
		return app.Route
	case len(app.Ports) > 0:
		return app.Ports[0]
	}
	return ""
}

type templateData struct {
	Stack       string
	Domain      string
	ProxyPort   string
	User        string
	Ports       map[string]int
	BinDir      string
	StackDir    string
	WorktreeDir string
}

func (controller *Controller) renderTemplate(stacks stackSet, appInstance instance, templateText string) (string, error) {
	portOf := func(name string, portNames ...string) (int, error) {
		if externalService, isExternal := controller.Config.External[name]; isExternal {
			return externalService.Port, nil
		}
		app, isApp := controller.anyDeclaration(appInstance.stackName, name)
		if !isApp {
			return 0, fmt.Errorf("port %q: no such app or external service", name)
		}
		portName := readyPortName(app)
		if len(portNames) > 0 {
			portName = portNames[0]
		}
		servingInstance := instance{stackName: controller.stackServing(stacks, appInstance.stackName, name), appName: name}
		port := cmp.Or(stacks.app(servingInstance).Ports[portName], controller.fixedPortsOf(servingInstance)[portName])
		if port == 0 {
			return 0, fmt.Errorf("%s needs the %s port of %q in stack %s, which is not started: start it or drop --no-deps", appInstance.processName(), portName, name, servingInstance.stackName)
		}
		return port, nil
	}
	repoCheckoutDir := func(repoName string) (string, error) {
		checkoutDir := stacks.byName[appInstance.stackName].CheckoutDirs[repoName]
		if checkoutDir == "" {
			return "", fmt.Errorf("%s needs repo %s, which is not cloned: clone it, or set repos.%s.path in ~/.config/ilot/env.yaml", appInstance.processName(), repoName, repoName)
		}
		return checkoutDir, nil
	}
	urlOf := func(name string) (string, error) {
		if _, isApp := controller.anyDeclaration(appInstance.stackName, name); !isApp {
			return "", fmt.Errorf("url %q: no such app", name)
		}
		return controller.publicBaseURL(instance{stackName: controller.stackServing(stacks, appInstance.stackName, name), appName: name}), nil
	}
	hostOf := func(name string) (string, error) {
		if _, isApp := controller.anyDeclaration(appInstance.stackName, name); !isApp {
			return "", fmt.Errorf("host %q: no such app", name)
		}
		targetInstance := instance{stackName: controller.stackServing(stacks, appInstance.stackName, name), appName: name}
		if controller.isLinkedContainer(appInstance, name) {
			targetInstance.stackName = appInstance.stackName
		}
		return controller.hostFor(appInstance, targetInstance), nil
	}
	templateFunctions := template.FuncMap{"port": portOf, "repo": repoCheckoutDir, "url": urlOf, "host": hostOf}
	parsed, err := template.New(appInstance.processName()).Funcs(templateFunctions).Option("missingkey=error").Parse(templateText)
	if err != nil {
		return "", err
	}
	var rendered strings.Builder
	checkoutDir, err := controller.checkoutDir(stacks, appInstance)
	if err != nil {
		return "", err
	}
	err = parsed.Execute(&rendered, templateData{
		Stack:       appInstance.stackName,
		Domain:      controller.Config.Domain,
		ProxyPort:   controller.ProxyPort,
		User:        controller.Login,
		Ports:       stacks.app(appInstance).Ports,
		BinDir:      controller.binDir(appInstance.stackName),
		StackDir:    controller.Store.StackDir(appInstance.stackName),
		WorktreeDir: checkoutDir,
	})
	if err != nil {
		return "", fmt.Errorf("render %q for %s: %w", templateText, appInstance.processName(), err)
	}
	return rendered.String(), nil
}
