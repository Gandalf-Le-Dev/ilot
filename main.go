package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/alecthomas/kong"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/ilotctl"
	"github.com/Gandalf-Le-Dev/ilot/internal/logs"
	"github.com/Gandalf-Le-Dev/ilot/internal/ports"
	"github.com/Gandalf-Le-Dev/ilot/internal/shellenv"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

type cli struct {
	Version kong.VersionFlag `short:"v" help:"Print the version of ilot and exit."`

	Ls      lsCommand      `cmd:"" help:"List the apps of every stack and the external services."`
	Apps    appsCommand    `cmd:"" help:"List the apps and the external services that the ilot.yaml files declare for the stack of the current worktree, with the dependencies of each app."`
	Start   startCommand   `cmd:"" help:"Build and start apps in the stack of the current worktree, after their dependencies."`
	Stop    stopCommand    `cmd:"" help:"Stop apps in the stack of the current worktree."`
	Boot    bootCommand    `cmd:"" help:"Start the autostart apps and the apps that ran before this machine restarted. Does nothing when the ilot daemon already runs."`
	Restart restartCommand `cmd:"" help:"Build the named apps again and restart them. With no app: stop every app of every stack and the ilot daemon, start a new daemon, then start the apps that ran and the autostart apps."`
	Env     envCommand     `cmd:"" help:"Print the environment of an app in the stack of the current worktree, with where each value comes from."`
	Logs    logsCommand    `cmd:"" help:"Print the log of an app in the stack of the current worktree. A shared app has one log for every stack."`
	Rm      rmCommand      `cmd:"" help:"Stop the apps of a stack and delete its state, its logs and its routes."`
	Clean   cleanCommand   `cmd:"" help:"Remove every stack whose worktrees are all deleted."`
	Stack   stackCommand   `cmd:"" help:"Print the stack of the current worktree, or put the worktree in another stack."`
	Check   checkCommand   `cmd:"" help:"Check an ilot.yaml with the ilot.yaml of the other repos: config rules and every template."`
	Skill   skillCommand   `cmd:"" help:"Print or install the ilot skill for AI agents."`
	Daemon  daemonCommand  `cmd:"" help:"Run the ilot daemon. ilot starts it when it needs it."`
}

type daemonCommand struct {
	Run daemonRunCommand `cmd:"" help:"Run the daemon that supervises the apps, until a signal or a shutdown call stops it."`
}

type daemonRunCommand struct {
	StateDir    string `required:"" help:"State folder of ilot, which holds the lock, the socket and the log of the daemon."`
	ProxyListen string `help:"Address of the proxy, for example 127.0.0.1:4580. Without it, the daemon runs no proxy."`
}

func (command *daemonRunCommand) Run(ctx context.Context) error {
	return daemon.Run(ctx, daemon.Options{Paths: daemon.Paths{StateDir: command.StateDir}, Version: versionOfThisBinary(), ProxyListen: command.ProxyListen})
}

type envCommand struct {
	App string `arg:"" help:"App declared in an ilot.yaml."`
}

func (command *envCommand) Run(ctx context.Context) error {
	controller, stackName, err := newControllerForCurrentWorktree(ctx, time.Minute, stackOfCurrentFolder)
	if err != nil {
		return err
	}
	entries, err := controller.Environment(stackName, command.App)
	if err != nil {
		return err
	}
	table := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, entry := range entries {
		fmt.Fprintf(table, "%s=%s\t(%s)\n", entry.Key, entry.Value, entry.Source)
	}
	return table.Flush()
}

type logsCommand struct {
	App    string `arg:"" help:"App declared in an ilot.yaml."`
	Follow bool   `short:"f" help:"Keep printing new lines."`
	Lines  int    `short:"n" default:"100" help:"How many of the last lines to print first."`
	JSON   bool   `help:"Print each line as the app wrote it. Without it, ilot turns a JSON line into one readable line."`
}

func (command *logsCommand) Run(ctx context.Context) error {
	controller, stackName, err := newControllerForCurrentWorktree(ctx, time.Minute, stackOfCurrentFolder)
	if err != nil {
		return err
	}
	logPath, err := controller.LogFile(stackName, command.App)
	if err != nil {
		return err
	}
	format := logs.Readable
	if command.JSON {
		format = logs.Raw
	}
	return logs.PrintMessages(ctx, logPath, command.Lines, command.Follow, format, os.Stdout)
}

type bootCommand struct {
	Checkout string        `required:"" type:"existingdir" help:"Main checkout of a repo of the workspace."`
	Timeout  time.Duration `default:"5m" help:"How long to wait for each app to be ready."`
}

type restartCommand struct {
	Apps    []string      `arg:"" optional:"" help:"Apps to build again and restart. With no app, ilot restarts its daemon and every app of every stack."`
	Stack   string        `help:"Stack of the apps. The default is the stack of the current folder. The watcher of an app uses it."`
	Timeout time.Duration `default:"5m" help:"How long to wait for each app to be ready."`
}

func (command *restartCommand) Run(ctx context.Context) error {
	switch {
	case len(command.Apps) == 0 && command.Stack != "":
		return errors.New("--stack needs the apps to restart")
	case len(command.Apps) == 0:
		controller, baseStackName, err := newControllerForCurrentWorktree(ctx, command.Timeout, baseStack)
		if err != nil {
			return err
		}
		return controller.Restart(ctx, baseStackName)
	case command.Stack != "":
		currentWorkspace, _, err := findCurrentWorkspace()
		if err != nil {
			return err
		}
		currentWorkspace, store, err := withBindings(currentWorkspace)
		if err != nil {
			return err
		}
		controller, err := newController(ctx, currentWorkspace, store, command.Stack, command.Timeout)
		if err != nil {
			return err
		}
		return controller.RestartApps(ctx, command.Stack, command.Apps)
	}
	controller, stackName, err := newControllerForCurrentWorktree(ctx, command.Timeout, stackOfCurrentFolder)
	if err != nil {
		return err
	}
	return controller.RestartApps(ctx, stackName, command.Apps)
}

type lsCommand struct {
	JSON bool `help:"Print the apps, the external services and the checkout of each repo per stack as one JSON object, for tools and agents."`
}

type lsJSONOutput struct {
	Home string `json:"home"`
	ilotctl.Listing
	CheckoutsByStack map[string]map[string]workspace.Worktree `json:"checkouts"`
}

type rmCommand struct {
	Stack string `arg:"" help:"Stack to remove, as ilot ls shows it."`
}

type cleanCommand struct{}

type startCommand struct {
	Apps    []string      `arg:"" help:"Apps declared in an ilot.yaml."`
	NoDeps  bool          `help:"Start only the named apps, not their dependencies."`
	Timeout time.Duration `default:"2m" help:"How long to wait for each app to be ready."`
}

type stopCommand struct {
	Apps []string `arg:"" help:"Apps declared in an ilot.yaml."`
}

func (command *bootCommand) Run(ctx context.Context) error {
	currentWorkspace, mainCheckout, err := findWorkspaceOf(command.Checkout)
	if err != nil {
		return err
	}
	if !mainCheckout.IsMain {
		return fmt.Errorf("%s is a linked worktree, not the main checkout", command.Checkout)
	}
	currentWorkspace, store, err := withBindings(currentWorkspace)
	if err != nil {
		return err
	}
	controller, err := newController(ctx, currentWorkspace, store, workspace.BaseStackName, command.Timeout)
	if err != nil {
		return err
	}
	return controller.Boot(ctx, workspace.BaseStackName)
}

func (command *lsCommand) Run(ctx context.Context) error {
	controller, _, err := newControllerForCurrentWorktree(ctx, time.Minute, baseStack)
	if err != nil {
		return err
	}
	if command.JSON {
		controller.Output = os.Stderr
	}
	listing, err := controller.List(ctx)
	if err != nil {
		return err
	}
	checkoutsByStack, err := controller.StackCheckouts()
	if err != nil {
		return err
	}
	if command.JSON {
		return json.NewEncoder(os.Stdout).Encode(lsJSONOutput{Home: controller.HomeURL(), Listing: listing, CheckoutsByStack: checkoutsByStack})
	}
	fmt.Printf("every URL: %s\n\n", controller.HomeURL())
	table := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "STACK\tAPP\tSTATUS\tPORTS\tURL")
	for _, app := range listing.Apps {
		var formattedPorts []string
		for _, portName := range slices.Sorted(maps.Keys(app.Ports)) {
			formattedPorts = append(formattedPorts, fmt.Sprintf("%s=%d", portName, app.Ports[portName]))
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", app.Stack, app.App, app.Status, strings.Join(formattedPorts, " "), app.URL)
	}
	for _, externalService := range listing.ExternalServices {
		fmt.Fprintf(table, "external\t%s\t%s\t%d\t\n", externalService.Name, externalService.Status, externalService.Port)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return printWorktreeCheckouts(os.Stdout, checkoutsByStack, homeDir)
}

// The main checkouts are the same for every stack, so only the worktrees of a stack say something.
func printWorktreeCheckouts(output io.Writer, checkoutsByStack map[string]map[string]workspace.Worktree, homeDir string) error {
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	hasRows := false
	for _, stackName := range slices.Sorted(maps.Keys(checkoutsByStack)) {
		for _, repoName := range slices.Sorted(maps.Keys(checkoutsByStack[stackName])) {
			checkout := checkoutsByStack[stackName][repoName]
			if checkout.IsMain {
				continue
			}
			if !hasRows {
				fmt.Fprintln(output)
				fmt.Fprintln(table, "CHECKOUT\tREPO\tFOLDER\tBRANCH")
				hasRows = true
			}
			folder := checkout.Dir
			if rest, isUnderHome := strings.CutPrefix(folder, homeDir+"/"); isUnderHome {
				folder = "~/" + rest
			}
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", stackName, repoName, folder, checkout.Branch)
		}
	}
	return table.Flush()
}

func (command *rmCommand) Run(ctx context.Context) error {
	controller, _, err := newControllerForCurrentWorktree(ctx, time.Minute, baseStack)
	if err != nil {
		return err
	}
	return controller.Remove(ctx, command.Stack)
}

func (command *cleanCommand) Run(ctx context.Context) error {
	controller, _, err := newControllerForCurrentWorktree(ctx, time.Minute, baseStack)
	if err != nil {
		return err
	}
	stacksWithoutWorktrees, err := controller.StacksWithoutWorktrees()
	if err != nil {
		return err
	}
	for _, stackName := range stacksWithoutWorktrees {
		if err := controller.Remove(ctx, stackName); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", stackName)
	}
	return nil
}

func (command *startCommand) Run(ctx context.Context) error {
	controller, stackName, err := newControllerForCurrentWorktree(ctx, command.Timeout, stackOfCurrentFolderWithBinding)
	if err != nil {
		return err
	}
	return controller.Start(ctx, stackName, command.Apps, ilotctl.StartOptions{SkipDependencies: command.NoDeps})
}

func (command *stopCommand) Run(ctx context.Context) error {
	controller, stackName, err := newControllerForCurrentWorktree(ctx, time.Minute, stackOfCurrentFolder)
	if err != nil {
		return err
	}
	return controller.Stop(ctx, stackName, command.Apps)
}

// Which stack a command works on.
type stackChoice int

const (
	// For a command that works on every stack.
	baseStack stackChoice = iota
	stackOfCurrentFolder
	// For ilot start: the first start in a worktree binds the worktree to its stack.
	stackOfCurrentFolderWithBinding
)

func newControllerForCurrentWorktree(ctx context.Context, readyTimeout time.Duration, choice stackChoice) (*ilotctl.Controller, string, error) {
	currentWorkspace, worktree, err := findCurrentWorkspace()
	if err != nil {
		return nil, "", err
	}
	currentWorkspace, store, err := withBindings(currentWorkspace)
	if err != nil {
		return nil, "", err
	}
	stackName := workspace.BaseStackName
	if choice != baseStack {
		currentWorkspace, stackName, err = stackOfWorktree(ctx, store, currentWorkspace, worktree, choice == stackOfCurrentFolderWithBinding, os.Stderr)
		if err != nil {
			return nil, "", err
		}
	}
	controller, err := newController(ctx, currentWorkspace, store, stackName, readyTimeout)
	return controller, stackName, err
}

func findCurrentWorkspace() (workspace.Workspace, workspace.Worktree, error) {
	currentDir, err := os.Getwd()
	if err != nil {
		return workspace.Workspace{}, workspace.Worktree{}, err
	}
	return findWorkspaceOf(currentDir)
}

func findWorkspaceOf(dir string) (workspace.Workspace, workspace.Worktree, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return workspace.Workspace{}, workspace.Worktree{}, err
	}
	global, err := config.LoadGlobal(homeDir)
	if err != nil {
		return workspace.Workspace{}, workspace.Worktree{}, err
	}
	return workspace.Find(dir, homeDir, global, workspace.NewGit())
}

func stateDirOf(homeDir string) string {
	return filepath.Join(cmp.Or(os.Getenv("XDG_STATE_HOME"), filepath.Join(homeDir, ".local", "state")), "ilot")
}

func newController(ctx context.Context, currentWorkspace workspace.Workspace, store state.Store, stackName string, readyTimeout time.Duration) (*ilotctl.Controller, error) {
	stackConfig, err := currentWorkspace.ConfigForStack(stackName)
	if err != nil {
		return nil, err
	}
	global := currentWorkspace.Global
	proxyListen := global.Proxy.Listen
	_, proxyPort, err := net.SplitHostPort(proxyListen)
	if err != nil {
		return nil, err
	}
	currentUser, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("find the name of the current user, for the User template: %w", err)
	}
	stateDir := store.RootDir
	stackStore := ilotctl.StackStore{Files: store, Workspace: currentWorkspace.Name, Root: currentWorkspace.RootDir}
	shellEnv := sync.OnceValues(func() (map[string]string, error) {
		return shellenv.OfUser(global.ShellEnv.Args).Environment(ctx)
	})
	ilotBinary, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find the ilot binary, to start its daemon: %w", err)
	}
	ilotEnv := map[string]string{}
	if value, isSet := os.LookupEnv("XDG_STATE_HOME"); isSet {
		ilotEnv["XDG_STATE_HOME"] = value
	}
	daemonPaths := daemon.Paths{StateDir: stateDir}
	startDaemon := func() error {
		_, err := daemon.Launch{Binary: ilotBinary, Args: []string{"daemon", "run", "--state-dir", stateDir, "--proxy-listen", proxyListen}, Env: nil, LogFile: daemonPaths.LogFile()}.Start()
		return err
	}
	controller := &ilotctl.Controller{
		Workspace:           currentWorkspace.Name,
		Config:              stackConfig,
		Store:               stackStore,
		Supervisor:          &ilotctl.DaemonSupervisor{Starter: daemon.Starter{Paths: daemonPaths, Spawn: startDaemon, Timeout: 0}, Version: versionOfThisBinary(), ProxyListen: proxyListen, Warnings: os.Stderr},
		RunBuild:            runBuild,
		EnsureImage:         ensureImage,
		IsPortFreeOnHost:    ports.IsFreeOnLoopback,
		IsExternalServiceUp: isListeningOnLocalhost,
		CheckoutsForStack:   currentWorkspace.CheckoutsForStack,
		ConfigForStack:      currentWorkspace.ConfigForStack,
		ReposNotCloned:      currentWorkspace.ReposNotCloned(),
		Settings:            currentWorkspace.Settings,
		Domain:              global.Proxy.Domain,
		PortRange:           ports.Range{First: global.Ports.First, Last: global.Ports.Last},
		ShellEnv:            shellEnv,
		Login:               currentUser.Username,
		ProxyPort:           proxyPort,
		IlotBinary:          ilotBinary,
		IlotEnv:             ilotEnv,
		ReadyTimeout:        readyTimeout,
		ProgressInterval:    10 * time.Second,
		Output:              os.Stdout,
	}
	return controller, nil
}

func runBuild(ctx context.Context, workingDir, shellCommand string, environ []string) error {
	command := exec.CommandContext(ctx, "bash", "-c", shellCommand)
	command.Dir = workingDir
	command.Env = environ
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	return command.Run()
}

func ensureImage(ctx context.Context, image string) (string, error) {
	if exec.CommandContext(ctx, "docker", "image", "inspect", image).Run() == nil {
		return "", nil
	}
	fmt.Fprintln(os.Stderr, "pulling "+image)
	return runCapturingOutput(ctx, "docker", "pull", image)
}

func runCapturingOutput(ctx context.Context, name string, arguments ...string) (string, error) {
	var capturedOutput bytes.Buffer
	teeToTerminal := io.MultiWriter(os.Stderr, &capturedOutput)
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stdout = teeToTerminal
	command.Stderr = teeToTerminal
	err := command.Run()
	return capturedOutput.String(), err
}

func isListeningOnLocalhost(port int) bool {
	connection, err := net.DialTimeout("tcp", net.JoinHostPort("localhost", strconv.Itoa(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	return connection.Close() == nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var commandLineArguments cli
	commandLine := kong.Parse(&commandLineArguments, kong.Name("ilot"), kong.Description("Run the apps of each git worktree behind one local proxy.\n\nAgents: if the ilot skill is not in your context, run ilot skill print."), kong.BindTo(ctx, (*context.Context)(nil)), kong.Vars{"version": versionOfThisBinary()})
	commandLine.FatalIfErrorf(commandLine.Run())
}
