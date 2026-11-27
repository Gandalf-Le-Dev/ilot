package ilotctl

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/Gandalf-Le-Dev/ilot/internal/appconfig"
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
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

func (controller *Controller) processSpec(stacks stackSet, appInstance instance) (ProcessSpec, error) {
	envEntries, err := controller.environmentOf(stacks, appInstance)
	if err != nil {
		return ProcessSpec{}, err
	}
	// The daemon gives a process this environment and nothing else.
	env := map[string]string{}
	var command string
	if controller.isContainer(appInstance) {
		// The docker command needs the shell values, for example PATH and HOME. The image gets only the -e values.
		for _, entry := range envEntries {
			if entry.Source == sourceShell {
				env[entry.Key] = entry.Value
			}
		}
		envEntries = containerEnvEntries(envEntries)
		command, err = controller.containerCommand(stacks, appInstance, envEntries)
	} else {
		command, err = controller.processCommand(stacks, appInstance)
	}
	if err != nil {
		return ProcessSpec{}, err
	}
	for _, entry := range envEntries {
		env[entry.Key] = entry.Value
	}
	if err := os.MkdirAll(filepath.Dir(controller.logFile(appInstance)), 0o700); err != nil {
		return ProcessSpec{}, err
	}
	workingDir, err := controller.workingDir(stacks, appInstance)
	if err != nil {
		return ProcessSpec{}, err
	}
	shutdownCommand, err := controller.shutdownCommand(stacks, appInstance)
	if err != nil {
		return ProcessSpec{}, err
	}
	return ProcessSpec{
		Name: appInstance.processName(),
		Spec: daemon.Spec{
			Command: command,
			Dir:     workingDir,
			Env:     env,
			LogFile: controller.logFile(appInstance),
			Stop:    shutdownCommand,
			Ready:   controller.readyProbe(stacks, appInstance),
		},
	}, nil
}

func (controller *Controller) processCommand(stacks stackSet, appInstance instance) (string, error) {
	command, err := controller.renderTemplate(stacks, appInstance, controller.app(appInstance).Run)
	if err != nil {
		return "", err
	}
	return controller.wrapped(appInstance, command), nil
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
	return controller.wrapped(appInstance, command), nil
}

// The wrap of a repo, for example "mise exec --", gives its commands the tools of the repo.
// The command goes to a shell as one word, because it can hold pipes and variables.
func (controller *Controller) wrapped(appInstance instance, shellCommand string) string {
	config, _ := controller.declaringConfig(appInstance)
	wrap := config.RepoWrap[cmp.Or(controller.app(appInstance).Repo, appconfig.DefaultRepo)]
	if wrap == "" {
		return shellCommand
	}
	return wrap + " sh -c " + shellQuote(shellCommand)
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

// The watcher runs ilot restart, so a change of a file goes through the same build lock and the same steps as a command of the user.
// It names the stack, because the folder of an app does not always tell it: a ticket stack can run an app from a main checkout.
func (controller *Controller) watcherSpec(watchedInstance instance, workingDir string) (ProcessSpec, bool, error) {
	watch := controller.app(watchedInstance).Watch
	if len(watch.Paths) == 0 {
		return ProcessSpec{}, false, nil
	}
	commandParts := []string{"watchexec --postpone"}
	for _, watchedPath := range watch.Paths {
		commandParts = append(commandParts, "--watch "+shellQuote(filepath.Join(workingDir, watchedPath)))
	}
	if len(watch.Exts) > 0 {
		commandParts = append(commandParts, "--exts "+strings.Join(watch.Exts, ","))
	}
	onChange := shellQuote(controller.IlotBinary) + " restart --stack " + shellQuote(watchedInstance.stackName) + " " + shellQuote(watchedInstance.appName)
	commandParts = append(commandParts, "--", shellQuote(onChange))
	shellEnv, err := controller.shellEnvironment()
	if err != nil {
		return ProcessSpec{}, false, err
	}
	env := maps.Clone(shellEnv)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, controller.IlotEnv)
	watcherInstance := instance{stackName: watchedInstance.stackName, appName: watchedInstance.appName + "-watch"}
	return ProcessSpec{
		Name: watcherInstance.processName(),
		Spec: daemon.Spec{
			Command: strings.Join(commandParts, " "),
			Dir:     workingDir,
			Env:     env,
			LogFile: controller.logFile(watcherInstance),
			Stop:    "",
			Ready:   nil,
		},
	}, true, nil
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
	case len(app.PortNames()) > 0:
		return app.PortNames()[0]
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
		return controller.hostFor(appInstance), nil
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
