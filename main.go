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

	"github.com/Gandalf-Le-Dev/ilot/internal/checkouts"
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/ilotctl"
	"github.com/Gandalf-Le-Dev/ilot/internal/logs"
	"github.com/Gandalf-Le-Dev/ilot/internal/ports"
	"github.com/Gandalf-Le-Dev/ilot/internal/shellenv"
	"github.com/Gandalf-Le-Dev/ilot/internal/stack"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

const (
	// The address of the proxy in the daemon. ILOT_PROXY_LISTEN moves it when another program has the port.
	defaultProxyListen  = "127.0.0.1:4580"
	proxyListenVariable = "ILOT_PROXY_LISTEN"
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

func (command *envCommand) Run() error {
	controller, worktree, err := newControllerForCurrentWorktree(time.Minute, false)
	if err != nil {
		return err
	}
	entries, err := controller.Environment(worktree, command.App)
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
	App    string `arg:"" help:"App declared in devbox/ilot.yaml."`
	Follow bool   `short:"f" help:"Keep printing new lines."`
	Lines  int    `short:"n" default:"100" help:"How many of the last lines to print first."`
	JSON   bool   `help:"Print each line as the app wrote it. Without it, ilot turns a JSON line into one readable line."`
}

func (command *logsCommand) Run(ctx context.Context) error {
	controller, worktree, err := newControllerForCurrentWorktree(time.Minute, false)
	if err != nil {
		return err
	}
	logPath, err := controller.LogFile(worktree, command.App)
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
	Checkout string        `required:"" type:"existingdir" help:"Main checkout of the platform repo, whose devbox/ilot.yaml is read."`
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
		controller, mainCheckout, err := newControllerForCurrentWorktree(command.Timeout, true)
		if err != nil {
			return err
		}
		return controller.Restart(ctx, mainCheckout)
	case command.Stack != "":
		currentWorkspace, err := loadCurrentWorkspace()
		if err != nil {
			return err
		}
		namedStack := stack.Worktree{Name: command.Stack, WorktreeDir: "", MainCheckoutDir: "", Branch: ""}
		controller, err := newController(currentWorkspace, namedStack, command.Timeout)
		if err != nil {
			return err
		}
		return controller.RestartApps(ctx, namedStack, command.Apps)
	}
	controller, worktree, err := newControllerForCurrentWorktree(command.Timeout, false)
	if err != nil {
		return err
	}
	return controller.RestartApps(ctx, worktree, command.Apps)
}

type lsCommand struct {
	JSON bool `help:"Print the apps, the external services and the checkout of each repo per stack as one JSON object, for tools and agents."`
}

type lsJSONOutput struct {
	Home string `json:"home"`
	ilotctl.Listing
	CheckoutsByStack map[string]map[string]checkouts.Checkout `json:"checkouts"`
}

type rmCommand struct {
	Stack string `arg:"" help:"Stack to remove, as ilot ls shows it."`
}

type cleanCommand struct{}

type startCommand struct {
	Apps    []string      `arg:"" help:"Apps declared in devbox/ilot.yaml."`
	NoDeps  bool          `help:"Start only the named apps, not their dependencies."`
	Timeout time.Duration `default:"2m" help:"How long to wait for each app to be ready."`
}

type stopCommand struct {
	Apps []string `arg:"" help:"Apps declared in devbox/ilot.yaml."`
}

func (command *bootCommand) Run(ctx context.Context) error {
	mainCheckout, err := stack.Locate(command.Checkout)
	if err != nil {
		return err
	}
	if mainCheckout.Name != stack.MasterStackName {
		return fmt.Errorf("%s is a linked worktree, not the main checkout", command.Checkout)
	}
	currentWorkspace, err := loadCurrentWorkspace()
	if err != nil {
		return err
	}
	controller, err := newController(currentWorkspace, mainCheckout, command.Timeout)
	if err != nil {
		return err
	}
	return controller.Boot(ctx, mainCheckout)
}

func (command *lsCommand) Run(ctx context.Context) error {
	controller, _, err := newControllerForCurrentWorktree(time.Minute, true)
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
func printWorktreeCheckouts(output io.Writer, checkoutsByStack map[string]map[string]checkouts.Checkout, homeDir string) error {
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	hasRows := false
	for _, stackName := range slices.Sorted(maps.Keys(checkoutsByStack)) {
		for _, repoName := range slices.Sorted(maps.Keys(checkoutsByStack[stackName])) {
			checkout := checkoutsByStack[stackName][repoName]
			if checkout.IsMainCheckout {
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
	controller, _, err := newControllerForCurrentWorktree(time.Minute, true)
	if err != nil {
		return err
	}
	return controller.Remove(ctx, command.Stack)
}

func (command *cleanCommand) Run(ctx context.Context) error {
	controller, _, err := newControllerForCurrentWorktree(time.Minute, true)
	if err != nil {
		return err
	}
	stacksWithoutCheckouts, err := controller.StacksWithoutCheckouts()
	if err != nil {
		return err
	}
	for _, stackName := range stacksWithoutCheckouts {
		if err := controller.Remove(ctx, stackName); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", stackName)
	}
	return nil
}

func (command *startCommand) Run(ctx context.Context) error {
	controller, worktree, err := newControllerForCurrentWorktree(command.Timeout, false)
	if err != nil {
		return err
	}
	return controller.Start(ctx, worktree, command.Apps, ilotctl.StartOptions{SkipDependencies: command.NoDeps})
}

func (command *stopCommand) Run(ctx context.Context) error {
	controller, worktree, err := newControllerForCurrentWorktree(time.Minute, false)
	if err != nil {
		return err
	}
	return controller.Stop(ctx, worktree, command.Apps)
}

func newControllerForCurrentWorktree(readyTimeout time.Duration, isCrossStack bool) (*ilotctl.Controller, stack.Worktree, error) {
	currentDir, err := currentFolder()
	if err != nil {
		return nil, stack.Worktree{}, err
	}
	currentWorkspace, err := loadCurrentWorkspace()
	if err != nil {
		return nil, stack.Worktree{}, err
	}
	worktree, err := worktreeForCommand(currentWorkspace, currentDir, isCrossStack, os.Stderr)
	if err != nil {
		return nil, stack.Worktree{}, err
	}
	controller, err := newController(currentWorkspace, worktree, readyTimeout)
	return controller, worktree, err
}

func worktreeForCommand(currentWorkspace workspace, currentDir string, isCrossStack bool, notes io.Writer) (stack.Worktree, error) {
	if isCrossStack {
		return platformMainCheckout(currentWorkspace)
	}
	location := currentWorkspace.locationOf(currentDir)
	if location == outsideAnyCheckout {
		fmt.Fprintln(notes, "not in a checkout: using the master stack")
		return platformMainCheckout(currentWorkspace)
	}
	if location == insideHomeCheckout {
		fmt.Fprintln(notes, "the home folder is a checkout, not a known repo: using the master stack")
		return platformMainCheckout(currentWorkspace)
	}
	if _, err := checkouts.RepoOf(currentWorkspace.repoPaths, currentDir); err != nil {
		return stack.Worktree{}, err
	}
	return stack.Locate(currentDir)
}

func loadCurrentWorkspace() (workspace, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return workspace{}, err
	}
	return loadWorkspace(homeDir)
}

func newController(currentWorkspace workspace, worktree stack.Worktree, readyTimeout time.Duration) (*ilotctl.Controller, error) {
	config, err := currentWorkspace.configForStack(worktree.Name)
	if err != nil {
		return nil, err
	}
	proxyListen := cmp.Or(os.Getenv(proxyListenVariable), defaultProxyListen)
	_, proxyPort, err := net.SplitHostPort(proxyListen)
	if err != nil {
		return nil, fmt.Errorf("%s=%q is not an address such as %s: %w", proxyListenVariable, proxyListen, defaultProxyListen, err)
	}
	currentUser, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("find the name of the current user, for the User template: %w", err)
	}
	stateDir := filepath.Join(cmp.Or(os.Getenv("XDG_STATE_HOME"), filepath.Join(currentWorkspace.homeDir, ".local", "state")), "ilot")
	shellEnv := sync.OnceValues(func() (map[string]string, error) { return shellenv.Login().Environment(context.Background()) })
	ilotBinary, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find the ilot binary, to start its daemon: %w", err)
	}
	ilotEnv := map[string]string{}
	for _, name := range []string{"XDG_STATE_HOME", proxyListenVariable} {
		if value, isSet := os.LookupEnv(name); isSet {
			ilotEnv[name] = value
		}
	}
	daemonPaths := daemon.Paths{StateDir: stateDir}
	startDaemon := func() error {
		_, err := daemon.Launch{Binary: ilotBinary, Args: []string{"daemon", "run", "--state-dir", stateDir, "--proxy-listen", proxyListen}, Env: nil, LogFile: daemonPaths.LogFile()}.Start()
		return err
	}
	controller := &ilotctl.Controller{
		Config:              config,
		Store:               state.Store{RootDir: stateDir},
		Supervisor:          &ilotctl.DaemonSupervisor{Starter: daemon.Starter{Paths: daemonPaths, Spawn: startDaemon, Timeout: 0}, Version: versionOfThisBinary(), ProxyListen: proxyListen, Warnings: os.Stderr},
		RunBuild:            runBuild,
		EnsureImage:         ensureImage,
		IsPortFreeOnHost:    ports.IsFreeOnLoopback,
		IsExternalServiceUp: isListeningOnLocalhost,
		CheckoutsForStack:   currentWorkspace.checkoutsForStack,
		ConfigForStack:      currentWorkspace.configForStack,
		ReposNotCloned:      currentWorkspace.reposNotCloned(),
		Personal:            currentWorkspace.settings,
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
