package plan

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

// Machine is what the rendering knows of this machine and of its user.
type Machine struct {
	// The state folder, which holds the folder of each stack.
	StateDir  string
	Domain    string
	ProxyPort string
	Login     string
	// The environment of a login shell, the base of the environment of every app.
	ShellEnv map[string]string
	// The watcher of an app runs this binary, with IlotEnv: the variables that make it find the same state and the same daemon.
	IlotBinary string
	IlotEnv    map[string]string
}

// ProcessSpec is a process as the daemon runs it, with its name.
type ProcessSpec struct {
	Name string
	daemon.Spec
}

// StackDir holds the logs and the binaries of a stack.
func (snapshot Snapshot) StackDir(stackName string) string {
	return state.Store{RootDir: snapshot.Machine.StateDir, LockTimeout: 0, OnLockWait: nil}.StackDir(snapshot.Workspace, stackName)
}

func (snapshot Snapshot) ProcessName(appInstance Instance) string {
	return ProcessName(snapshot.Workspace, appInstance)
}

// ProcessName is the name of an instance in the daemon. The workspace is in the name, because the daemon runs the apps of every workspace.
func ProcessName(workspaceName string, appInstance Instance) string {
	return appInstance.App + "." + appInstance.Stack + "." + workspaceName
}

func (snapshot Snapshot) CheckoutDir(appInstance Instance) (string, error) {
	repoName := cmp.Or(snapshot.App(appInstance).Repo, snapshot.Workspace)
	checkoutDir := snapshot.Checkouts[appInstance.Stack][repoName]
	if checkoutDir == "" {
		return "", fmt.Errorf("%s needs repo %s, which is not cloned: clone it, or set %s", snapshot.ProcessName(appInstance), repoName, snapshot.RepoPathKey(repoName))
	}
	return checkoutDir, nil
}

func (snapshot Snapshot) BinDir(stackName string) string {
	return filepath.Join(snapshot.StackDir(stackName), "bin")
}

func (snapshot Snapshot) LogFile(appInstance Instance) string {
	return filepath.Join(snapshot.StackDir(appInstance.Stack), "logs", appInstance.App+".log")
}

func (snapshot Snapshot) Spec(appInstance Instance) (ProcessSpec, error) {
	envEntries, err := snapshot.Environment(appInstance)
	if err != nil {
		return ProcessSpec{}, err
	}
	// The daemon gives a process this environment and nothing else.
	env := map[string]string{}
	var command string
	if snapshot.IsContainer(appInstance) {
		// The docker command needs the shell values, for example PATH and HOME. The image gets only the -e values.
		for _, entry := range envEntries {
			if entry.Source == sourceShell {
				env[entry.Key] = entry.Value
			}
		}
		envEntries = containerEnvEntries(envEntries)
		command, err = snapshot.containerCommand(appInstance, envEntries)
	} else {
		command, err = snapshot.processCommand(appInstance)
	}
	if err != nil {
		return ProcessSpec{}, err
	}
	for _, entry := range envEntries {
		env[entry.Key] = entry.Value
	}
	workingDir, err := snapshot.WorkingDir(appInstance)
	if err != nil {
		return ProcessSpec{}, err
	}
	shutdownCommand, err := snapshot.shutdownCommand(appInstance)
	if err != nil {
		return ProcessSpec{}, err
	}
	return ProcessSpec{
		Name: snapshot.ProcessName(appInstance),
		Spec: daemon.Spec{
			Command: command,
			Dir:     workingDir,
			Env:     env,
			LogFile: snapshot.LogFile(appInstance),
			Stop:    shutdownCommand,
			Ready:   snapshot.readyProbe(appInstance),
		},
	}, nil
}

func (snapshot Snapshot) processCommand(appInstance Instance) (string, error) {
	command, err := snapshot.Render(appInstance, snapshot.App(appInstance).Run)
	if err != nil {
		return "", err
	}
	return snapshot.Wrapped(appInstance, command), nil
}

func (snapshot Snapshot) shutdownCommand(appInstance Instance) (string, error) {
	if snapshot.IsContainer(appInstance) {
		return snapshot.containerStopCommand(appInstance), nil
	}
	stop := snapshot.App(appInstance).Stop
	if stop == "" {
		return "", nil
	}
	command, err := snapshot.Render(appInstance, stop)
	if err != nil {
		return "", err
	}
	return snapshot.Wrapped(appInstance, command), nil
}

// The wrap of a repo, for example "mise exec --", gives its commands the tools of the repo.
// The command goes to a shell as one word, because it can hold pipes and variables.
func (snapshot Snapshot) Wrapped(appInstance Instance, shellCommand string) string {
	config, _ := snapshot.DeclaringConfig(appInstance)
	wrap := config.RepoWrap[cmp.Or(snapshot.App(appInstance).Repo, snapshot.Workspace)]
	if wrap == "" {
		return shellCommand
	}
	return wrap + " sh -c " + ShellQuote(shellCommand)
}

func (snapshot Snapshot) WorkingDir(appInstance Instance) (string, error) {
	appDir := snapshot.App(appInstance).Dir
	if filepath.IsAbs(appDir) {
		return appDir, nil
	}
	checkoutDir, err := snapshot.CheckoutDir(appInstance)
	if err != nil {
		return "", err
	}
	return filepath.Join(checkoutDir, appDir), nil
}

// Watcher gives the watcher process of an app that has watch paths.
func (snapshot Snapshot) Watcher(watchedInstance Instance) (ProcessSpec, bool, error) {
	watch := snapshot.App(watchedInstance).Watch
	if len(watch.Paths) == 0 {
		return ProcessSpec{}, false, nil
	}
	workingDir, err := snapshot.WorkingDir(watchedInstance)
	if err != nil {
		return ProcessSpec{}, false, err
	}
	checkoutDir, err := snapshot.CheckoutDir(watchedInstance)
	if err != nil {
		return ProcessSpec{}, false, err
	}
	commandParts := []string{"watchexec --postpone"}
	for _, watchedPath := range watch.Paths {
		commandParts = append(commandParts, "--watch "+ShellQuote(filepath.Join(workingDir, watchedPath)))
	}
	if len(watch.Exts) > 0 {
		commandParts = append(commandParts, "--exts "+strings.Join(watch.Exts, ","))
	}
	// ilot finds the workspace from the repo of its folder, and the working folder of an app can be outside every repo.
	onChange := "cd " + ShellQuote(checkoutDir) + " && " + ShellQuote(snapshot.Machine.IlotBinary) + " restart --stack " + ShellQuote(watchedInstance.Stack) + " " + ShellQuote(watchedInstance.App)
	commandParts = append(commandParts, "--", ShellQuote(onChange))
	env := map[string]string{}
	maps.Copy(env, snapshot.Machine.ShellEnv)
	maps.Copy(env, snapshot.Machine.IlotEnv)
	watcherInstance := Instance{Stack: watchedInstance.Stack, App: watchedInstance.App + "-watch"}
	return ProcessSpec{
		Name: snapshot.ProcessName(watcherInstance),
		Spec: daemon.Spec{
			Command: strings.Join(commandParts, " "),
			Dir:     workingDir,
			Env:     env,
			LogFile: snapshot.LogFile(watcherInstance),
			Stop:    "",
			Ready:   nil,
		},
	}, true, nil
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

func (snapshot Snapshot) Render(appInstance Instance, templateText string) (string, error) {
	portOf := func(name string, portNames ...string) (int, error) {
		if externalService, isExternal := snapshot.External(name); isExternal {
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
		servingInstance := Instance{Stack: snapshot.Serving(appInstance.Stack, name), App: name}
		port := cmp.Or(snapshot.Ports(servingInstance)[portName], snapshot.FixedPorts(servingInstance)[portName])
		if port == 0 {
			return 0, fmt.Errorf("%s needs the %s port of %q in stack %s, which is not started: start it or drop --no-deps", snapshot.ProcessName(appInstance), portName, name, servingInstance.Stack)
		}
		return port, nil
	}
	repoCheckoutDir := func(repoName string) (string, error) {
		checkoutDir := snapshot.Checkouts[appInstance.Stack][repoName]
		if checkoutDir == "" {
			return "", fmt.Errorf("%s needs repo %s, which is not cloned: clone it, or set %s", snapshot.ProcessName(appInstance), repoName, snapshot.RepoPathKey(repoName))
		}
		return checkoutDir, nil
	}
	urlOf := func(name string) (string, error) {
		if _, isApp := snapshot.AnyDeclaration(appInstance.Stack, name); !isApp {
			return "", fmt.Errorf("url %q: no such app", name)
		}
		return snapshot.baseURL(Instance{Stack: snapshot.Serving(appInstance.Stack, name), App: name}), nil
	}
	hostOf := func(name string) (string, error) {
		if _, isApp := snapshot.AnyDeclaration(appInstance.Stack, name); !isApp {
			return "", fmt.Errorf("host %q: no such app", name)
		}
		return snapshot.hostFor(appInstance), nil
	}
	templateFunctions := template.FuncMap{"port": portOf, "repo": repoCheckoutDir, "url": urlOf, "host": hostOf}
	parsed, err := template.New(snapshot.ProcessName(appInstance)).Funcs(templateFunctions).Option("missingkey=error").Parse(templateText)
	if err != nil {
		return "", err
	}
	var rendered strings.Builder
	checkoutDir, err := snapshot.CheckoutDir(appInstance)
	if err != nil {
		return "", err
	}
	err = parsed.Execute(&rendered, templateData{
		Workspace:   snapshot.Workspace,
		Stack:       appInstance.Stack,
		Domain:      snapshot.Machine.Domain,
		ProxyPort:   snapshot.Machine.ProxyPort,
		User:        snapshot.Machine.Login,
		Ports:       snapshot.Ports(appInstance),
		BinDir:      snapshot.BinDir(appInstance.Stack),
		StackDir:    snapshot.StackDir(appInstance.Stack),
		WorktreeDir: checkoutDir,
	})
	if err != nil {
		return "", fmt.Errorf("render %q for %s: %w", templateText, snapshot.ProcessName(appInstance), err)
	}
	return rendered.String(), nil
}

// The name that Docker gives to this machine, as a container sees it.
const hostNameInContainer = "host.docker.internal"

// The workspace is in the name, because two workspaces can have an app of one name in the base stack.
// An underscore separates the parts, because a name of an app, a stack or a workspace can hold a dash and never an underscore.
func (snapshot Snapshot) ContainerName(appInstance Instance) string {
	return "ilot_" + appInstance.App + "_" + appInstance.Stack + "_" + snapshot.Workspace
}

func (snapshot Snapshot) IsContainer(appInstance Instance) bool {
	return !snapshot.App(appInstance).Container.IsZero()
}

// The leftover of a crash or a killed ilot holds the name, so the command removes it before running.
// Each port is published on the loopback address, so the proxy and the other apps reach a container as they reach a process.
func (snapshot Snapshot) containerCommand(appInstance Instance, envEntries []EnvEntry) (string, error) {
	container := snapshot.App(appInstance).Container
	name := ShellQuote(snapshot.ContainerName(appInstance))
	commandParts := []string{
		"docker rm -f " + name + " >/dev/null 2>&1 || true && exec docker run --rm --name " + name,
		// Docker Desktop knows this name. Docker on Linux needs the flag.
		"--add-host " + ShellQuote(hostNameInContainer+":host-gateway"),
	}
	stackPorts := snapshot.Ports(appInstance)
	for _, portName := range slices.Sorted(maps.Keys(container.Ports)) {
		if stackPorts[portName] == 0 {
			return "", fmt.Errorf("%s has no port for %q yet", snapshot.ProcessName(appInstance), portName)
		}
		commandParts = append(commandParts, fmt.Sprintf("-p 127.0.0.1:%d:%d", stackPorts[portName], container.Ports[portName]))
	}
	for _, envFile := range container.EnvFiles {
		commandParts = append(commandParts, "--env-file "+ShellQuote(envFile))
	}
	for _, entry := range envEntries {
		commandParts = append(commandParts, "-e "+ShellQuote(entry.Key))
	}
	for _, sourceTemplate := range slices.Sorted(maps.Keys(container.Mounts)) {
		source, err := snapshot.Render(appInstance, sourceTemplate)
		if err != nil {
			return "", err
		}
		commandParts = append(commandParts, "-v "+ShellQuote(source+":"+container.Mounts[sourceTemplate]))
	}
	for _, argumentTemplate := range container.Args {
		argument, err := snapshot.Render(appInstance, argumentTemplate)
		if err != nil {
			return "", err
		}
		commandParts = append(commandParts, ShellQuote(argument))
	}
	commandParts = append(commandParts, ShellQuote(container.Image))
	return strings.Join(commandParts, " "), nil
}

// docker run only forwards SIGTERM, and Postgres answers it by waiting for its clients: docker stop kills after 10 s.
func (snapshot Snapshot) containerStopCommand(appInstance Instance) string {
	return "docker stop --time 10 " + ShellQuote(snapshot.ContainerName(appInstance))
}

// The env of the workspace in the global file is for processes: it can hold tokens that third-party images have no business reading. The shell is for the docker command, not for the image.
func containerEnvEntries(envEntries []EnvEntry) []EnvEntry {
	return slices.DeleteFunc(slices.Clone(envEntries), func(entry EnvEntry) bool {
		return entry.Source == sourceShell || entry.Source == sourceGlobal
	})
}

// With no probe, the daemon calls a process ready when it stays alive.
func (snapshot Snapshot) readyProbe(appInstance Instance) *daemon.Probe {
	app := snapshot.App(appInstance)
	probe := daemon.Probe{Host: "", Port: snapshot.Ports(appInstance)[readyPortName(app)], Path: app.Ready.Path, Command: app.Ready.Command}
	if snapshot.IsContainer(appInstance) && app.Ready.Command != "" {
		probe.Command = "docker exec " + ShellQuote(snapshot.ContainerName(appInstance)) + " sh -c " + ShellQuote(app.Ready.Command)
	}
	if probe.Validate() != nil {
		return nil
	}
	return &probe
}

// Every app listens on a loopback port of this machine. A process reaches it on localhost, and a container through the name of the machine.
func (snapshot Snapshot) hostFor(callerInstance Instance) string {
	if snapshot.IsContainer(callerInstance) {
		return hostNameInContainer
	}
	return "localhost"
}

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

// The layers, and the last one wins: the login shell, the env of the root repo, the env of the repo of the app, the env of the app, the global file.
// A value written with a template is computed by ilot (ports, addresses, the stack), so a value of the global file cannot override it.
func (snapshot Snapshot) Environment(appInstance Instance) ([]EnvEntry, error) {
	app := snapshot.App(appInstance)
	entryByKey := map[string]EnvEntry{}
	setAll := func(source string, valueByKey map[string]string) {
		for key, value := range valueByKey {
			entryByKey[key] = EnvEntry{Key: key, Value: value, Source: source}
		}
	}
	setAll(sourceShell, snapshot.Machine.ShellEnv)
	literalRootEnv, templatedRootEnv := splitByTemplate(snapshot.repoEnvOf(appInstance)[snapshot.Workspace])
	literalRepoEnv, templatedRepoEnv := splitByTemplate(snapshot.repoEnvOf(appInstance)[cmp.Or(app.Repo, snapshot.Workspace)])
	literalAppEnv, templatedAppEnv := splitByTemplate(app.Env)
	setAll(sourceRoot, literalRootEnv)
	setAll(sourceRepo, literalRepoEnv)
	setAll(sourceApp, literalAppEnv)
	setAll(sourceGlobal, snapshot.Settings.Env)
	setAll(sourceGlobalApp, snapshot.Settings.Apps[appInstance.App])
	for _, templatedEnv := range []map[string]string{templatedRootEnv, templatedRepoEnv, templatedAppEnv} {
		for _, key := range slices.Sorted(maps.Keys(templatedEnv)) {
			value, err := snapshot.Render(appInstance, templatedEnv[key])
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

// A stopped app keeps its route, so that its URL answers with the command that starts it.
func (snapshot Snapshot) Routes() []daemon.Route {
	var routes []daemon.Route
	stacks := snapshot.State.Stacks(snapshot.Workspace)
	for _, stackName := range slices.Sorted(maps.Keys(stacks)) {
		for _, appName := range slices.Sorted(maps.Keys(stacks[stackName].Apps)) {
			routedInstance := Instance{Stack: stackName, App: appName}
			app := snapshot.App(routedInstance)
			port := stacks[stackName].Apps[appName].Ports[app.Route]
			if app.Route == "" || port == 0 {
				continue
			}
			startHint := "ilot start " + appName
			if checkoutDir, err := snapshot.CheckoutDir(routedInstance); err == nil {
				startHint = "cd " + ShellQuote(checkoutDir) + " && " + startHint
			}
			routes = append(routes, daemon.Route{Host: snapshot.Host(routedInstance), Port: port, HomePath: app.HomePath, StartHint: startHint})
		}
	}
	return routes
}

func (snapshot Snapshot) URL(appInstance Instance) string {
	if snapshot.App(appInstance).Route == "" {
		return ""
	}
	return snapshot.baseURL(appInstance) + snapshot.App(appInstance).HomePath
}

func (snapshot Snapshot) Host(appInstance Instance) string {
	return appInstance.App + "." + appInstance.Stack + "." + snapshot.Workspace + "." + snapshot.Machine.Domain
}

func (snapshot Snapshot) baseURL(appInstance Instance) string {
	return snapshot.Machine.proxyURL(snapshot.Host(appInstance))
}

func (machine Machine) proxyURL(host string) string {
	url := "http://" + host
	if machine.ProxyPort != "" {
		url += ":" + machine.ProxyPort
	}
	return url
}

// HomeURL is the page of the proxy that lists every route: the proxy gives it for a host that is no app.
func (machine Machine) HomeURL() string {
	return machine.proxyURL(machine.Domain)
}

// ShellQuote gives a word that a shell reads as one word, whatever it holds.
func ShellQuote(word string) string {
	return "'" + strings.ReplaceAll(word, "'", `'"'"'`) + "'"
}

func (snapshot Snapshot) repoEnvOf(appInstance Instance) map[string]map[string]string {
	declaringConfig, _ := snapshot.DeclaringConfig(appInstance)
	return declaringConfig.RepoEnv
}
