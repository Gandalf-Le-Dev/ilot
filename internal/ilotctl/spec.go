package ilotctl

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

func (controller *Controller) binDir(stackName string) string {
	return filepath.Join(controller.StackDir(stackName), "bin")
}

func (controller *Controller) LogFile(stackName string, appName string) (string, error) {
	snapshot, _ := controller.snapshotOf(state.New(), stackName)
	appInstances, err := snapshot.Instances(stackName, []string{appName})
	if err != nil {
		return "", err
	}
	return controller.logFile(appInstances[0]), nil
}

func (controller *Controller) logFile(appInstance plan.Instance) string {
	return filepath.Join(controller.StackDir(appInstance.Stack), "logs", appInstance.App+".log")
}

func (controller *Controller) processSpec(snapshot plan.Snapshot, appInstance plan.Instance) (ProcessSpec, error) {
	envEntries, err := controller.environmentOf(snapshot, appInstance)
	if err != nil {
		return ProcessSpec{}, err
	}
	// The daemon gives a process this environment and nothing else.
	env := map[string]string{}
	var command string
	if controller.isContainer(snapshot, appInstance) {
		// The docker command needs the shell values, for example PATH and HOME. The image gets only the -e values.
		for _, entry := range envEntries {
			if entry.Source == sourceShell {
				env[entry.Key] = entry.Value
			}
		}
		envEntries = containerEnvEntries(envEntries)
		command, err = controller.containerCommand(snapshot, appInstance, envEntries)
	} else {
		command, err = controller.processCommand(snapshot, appInstance)
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
	workingDir, err := controller.workingDir(snapshot, appInstance)
	if err != nil {
		return ProcessSpec{}, err
	}
	shutdownCommand, err := controller.shutdownCommand(snapshot, appInstance)
	if err != nil {
		return ProcessSpec{}, err
	}
	return ProcessSpec{
		Name: controller.processName(appInstance),
		Spec: daemon.Spec{
			Command: command,
			Dir:     workingDir,
			Env:     env,
			LogFile: controller.logFile(appInstance),
			Stop:    shutdownCommand,
			Ready:   controller.readyProbe(snapshot, appInstance),
		},
	}, nil
}

func (controller *Controller) processCommand(snapshot plan.Snapshot, appInstance plan.Instance) (string, error) {
	command, err := controller.renderTemplate(snapshot, appInstance, snapshot.App(appInstance).Run)
	if err != nil {
		return "", err
	}
	return controller.wrapped(snapshot, appInstance, command), nil
}

func (controller *Controller) shutdownCommand(snapshot plan.Snapshot, appInstance plan.Instance) (string, error) {
	if controller.isContainer(snapshot, appInstance) {
		return controller.containerStopCommand(appInstance), nil
	}
	stop := snapshot.App(appInstance).Stop
	if stop == "" {
		return "", nil
	}
	command, err := controller.renderTemplate(snapshot, appInstance, stop)
	if err != nil {
		return "", err
	}
	return controller.wrapped(snapshot, appInstance, command), nil
}

// The wrap of a repo, for example "mise exec --", gives its commands the tools of the repo.
// The command goes to a shell as one word, because it can hold pipes and variables.
func (controller *Controller) wrapped(snapshot plan.Snapshot, appInstance plan.Instance, shellCommand string) string {
	config, _ := snapshot.DeclaringConfig(appInstance)
	wrap := config.RepoWrap[cmp.Or(snapshot.App(appInstance).Repo, controller.Workspace)]
	if wrap == "" {
		return shellCommand
	}
	return wrap + " sh -c " + shellQuote(shellCommand)
}

func (controller *Controller) workingDir(snapshot plan.Snapshot, appInstance plan.Instance) (string, error) {
	appDir := snapshot.App(appInstance).Dir
	if filepath.IsAbs(appDir) {
		return appDir, nil
	}
	checkoutDir, err := controller.checkoutDir(snapshot, appInstance)
	if err != nil {
		return "", err
	}
	return filepath.Join(checkoutDir, appDir), nil
}

// The watcher runs ilot restart, so a change of a file goes through the same build lock and the same steps as a command of the user.
// It names the stack, because the folder of an app does not always tell it: a ticket stack can run an app from a main checkout.
func (controller *Controller) watcherSpec(snapshot plan.Snapshot, watchedInstance plan.Instance, workingDir, checkoutDir string) (ProcessSpec, bool, error) {
	watch := snapshot.App(watchedInstance).Watch
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
	// ilot finds the workspace from the repo of its folder, and the working folder of an app can be outside every repo.
	onChange := "cd " + shellQuote(checkoutDir) + " && " + shellQuote(controller.IlotBinary) + " restart --stack " + shellQuote(watchedInstance.Stack) + " " + shellQuote(watchedInstance.App)
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
	watcherInstance := plan.Instance{Stack: watchedInstance.Stack, App: watchedInstance.App + "-watch"}
	return ProcessSpec{
		Name: controller.processName(watcherInstance),
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

func readyPortName(app config.App) string {
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
	Workspace   string
	Stack       string
	Domain      string
	ProxyPort   string
	User        string
	Ports       map[string]int
	BinDir      string
	StackDir    string
	WorktreeDir string
}

func (controller *Controller) renderTemplate(snapshot plan.Snapshot, appInstance plan.Instance, templateText string) (string, error) {
	portOf := func(name string, portNames ...string) (int, error) {
		if externalService, isExternal := controller.Config.External[name]; isExternal {
			return externalService.Port, nil
		}
		app, isApp := snapshot.AnyDeclaration(appInstance.Stack, name)
		if !isApp {
			return 0, fmt.Errorf("port %q: no such app or external service", name)
		}
		portName := readyPortName(app)
		if len(portNames) > 0 {
			portName = portNames[0]
		}
		servingInstance := plan.Instance{Stack: snapshot.Serving(appInstance.Stack, name), App: name}
		port := cmp.Or(snapshot.Ports(servingInstance)[portName], snapshot.FixedPorts(servingInstance)[portName])
		if port == 0 {
			return 0, fmt.Errorf("%s needs the %s port of %q in stack %s, which is not started: start it or drop --no-deps", controller.processName(appInstance), portName, name, servingInstance.Stack)
		}
		return port, nil
	}
	repoCheckoutDir := func(repoName string) (string, error) {
		checkoutDir := snapshot.Checkouts[appInstance.Stack][repoName]
		if checkoutDir == "" {
			return "", fmt.Errorf("%s needs repo %s, which is not cloned: clone it, or set %s", controller.processName(appInstance), repoName, snapshot.RepoPathKey(repoName))
		}
		return checkoutDir, nil
	}
	urlOf := func(name string) (string, error) {
		if _, isApp := snapshot.AnyDeclaration(appInstance.Stack, name); !isApp {
			return "", fmt.Errorf("url %q: no such app", name)
		}
		return controller.publicBaseURL(plan.Instance{Stack: snapshot.Serving(appInstance.Stack, name), App: name}), nil
	}
	hostOf := func(name string) (string, error) {
		if _, isApp := snapshot.AnyDeclaration(appInstance.Stack, name); !isApp {
			return "", fmt.Errorf("host %q: no such app", name)
		}
		return controller.hostFor(snapshot, appInstance), nil
	}
	templateFunctions := template.FuncMap{"port": portOf, "repo": repoCheckoutDir, "url": urlOf, "host": hostOf}
	parsed, err := template.New(controller.processName(appInstance)).Funcs(templateFunctions).Option("missingkey=error").Parse(templateText)
	if err != nil {
		return "", err
	}
	var rendered strings.Builder
	checkoutDir, err := controller.checkoutDir(snapshot, appInstance)
	if err != nil {
		return "", err
	}
	err = parsed.Execute(&rendered, templateData{
		Workspace:   controller.Workspace,
		Stack:       appInstance.Stack,
		Domain:      controller.Domain,
		ProxyPort:   controller.ProxyPort,
		User:        controller.Login,
		Ports:       snapshot.Ports(appInstance),
		BinDir:      controller.binDir(appInstance.Stack),
		StackDir:    controller.StackDir(appInstance.Stack),
		WorktreeDir: checkoutDir,
	})
	if err != nil {
		return "", fmt.Errorf("render %q for %s: %w", templateText, controller.processName(appInstance), err)
	}
	return rendered.String(), nil
}
