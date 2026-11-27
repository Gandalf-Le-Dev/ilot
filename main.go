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
	"github.com/Gandalf-Le-Dev/ilot/internal/engine"
	"github.com/Gandalf-Le-Dev/ilot/internal/logs"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
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
	Restart restartCommand `cmd:"" help:"Build the named apps again and restart them, in the stack of the current worktree."`
	Env     envCommand     `cmd:"" help:"Print the environment of an app in the stack of the current worktree, with where each value comes from."`
	Logs    logsCommand    `cmd:"" help:"Print the log of an app in the stack of the current worktree. A shared app has one log for every stack."`
	Rm      rmCommand      `cmd:"" help:"Stop the apps of a stack and delete its state, its logs and its routes."`
	Clean   cleanCommand   `cmd:"" help:"Remove every stack whose worktrees are all deleted."`
	Stack   stackCommand   `cmd:"" help:"Print the stack of the current worktree, or put the worktree in another stack."`
	Check   checkCommand   `cmd:"" help:"Check an ilot.yaml with the ilot.yaml of the other repos: config rules and every template."`
	Doctor  doctorCommand  `cmd:"" help:"Test what ilot needs on this machine: git, the state folder, the daemon, the proxy address, the name lookup, the login shell, and watchexec and docker when an app needs them. Report what an older ilot left behind."`
	Skill   skillCommand   `cmd:"" help:"Print or install the ilot skill for AI agents."`
	Daemon  daemonCommand  `cmd:"" help:"Show, stop or restart the ilot daemon, which supervises the apps of every stack and holds the proxy. ilot starts it when it needs it."`
}

type daemonCommand struct {
	Status  daemonStatusCommand  `cmd:"" help:"Print the version, the process ID and the proxy address of the ilot daemon."`
	Stop    daemonStopCommand    `cmd:"" help:"Stop every app of every stack, then the ilot daemon. The stacks keep their apps: ilot start --all starts them again."`
	Restart daemonRestartCommand `cmd:"" help:"Stop every app of every stack and the ilot daemon, start a new daemon, then start again every app that a stack of any workspace owns. A workspace that fails stays down, and the error names it. Use it after an upgrade of ilot or a change of proxy in the global file."`
	Run     daemonRunCommand     `cmd:"" hidden:"" help:"Run the daemon that supervises the apps, until a signal or a shutdown call stops it. ilot runs it by itself."`
}

type daemonRunCommand struct {
	StateDir    string `required:"" help:"State folder of ilot, which holds the lock, the socket and the log of the daemon."`
	ProxyListen string `help:"Address of the proxy, for example 127.0.0.1:4580. Without it, the daemon runs no proxy."`
}

func (command *daemonRunCommand) Run(ctx context.Context) error {
	return daemon.Run(ctx, daemon.Options{Paths: daemon.Paths{StateDir: command.StateDir}, Version: versionOfThisBinary(), ProxyListen: command.ProxyListen})
}

type envCommand struct {
	App       string    `arg:"" help:"App declared in an ilot.yaml."`
	Selection selection `embed:""`
}

func (command *envCommand) Run(ctx context.Context) error {
	source, stackName, err := sourceFor(ctx, command.Selection)
	if err != nil {
		return err
	}
	entries, err := source.Environment(stackName, command.App)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		fmt.Fprintf(os.Stdout, "%s=%s  (%s)\n", entry.Key, entry.Value, entry.Source)
	}
	return nil
}

type logsCommand struct {
	App       string    `arg:"" help:"App declared in an ilot.yaml."`
	Follow    bool      `short:"f" help:"Keep printing new lines."`
	Lines     int       `short:"n" default:"100" help:"How many of the last lines to print first."`
	JSON      bool      `help:"Print each line as the app wrote it. Without it, ilot turns a JSON line into one readable line."`
	Selection selection `embed:""`
}

func (command *logsCommand) Run(ctx context.Context) error {
	source, stackName, err := sourceFor(ctx, command.Selection)
	if err != nil {
		return err
	}
	logPath, err := source.LogFile(stackName, command.App)
	if err != nil {
		return err
	}
	format := logs.Readable
	if command.JSON {
		format = logs.Raw
	}
	return logs.PrintMessages(ctx, logPath, command.Lines, command.Follow, format, os.Stdout)
}

type restartCommand struct {
	Apps      []string      `arg:"" help:"Apps to build again and restart."`
	Timeout   time.Duration `default:"5m" help:"How long to wait for each app to be ready."`
	Selection selection     `embed:""`
}

func (command *restartCommand) Run(ctx context.Context) error {
	executor, stackName, err := newExecutorFor(ctx, command.Selection, command.Timeout, stackOfCurrentFolder)
	if err != nil {
		return err
	}
	return executor.RestartApps(ctx, stackName, command.Apps)
}

type lsCommand struct {
	JSON      bool               `help:"Print the apps, the external services and the checkout of each repo per stack as one JSON object, for tools and agents."`
	Selection workspaceSelection `embed:""`
}

type lsJSONOutput struct {
	Home string `json:"home"`
	engine.Listing
	CheckoutsByStack map[string]map[string]workspace.Worktree `json:"checkouts"`
}

type rmCommand struct {
	Stack     string             `arg:"" help:"Stack to remove, as ilot ls shows it."`
	Selection workspaceSelection `embed:""`
}

type cleanCommand struct {
	DryRun    bool               `help:"Print what ilot clean would remove, and remove nothing."`
	Selection workspaceSelection `embed:""`
}

type startCommand struct {
	Apps      []string      `arg:"" optional:"" help:"Apps declared in an ilot.yaml. With no app, ilot starts every app that the stack owns and that does not run."`
	All       bool          `help:"Start every app that a stack of the workspace owns and that does not run, for example after the daemon crashed."`
	NoDeps    bool          `help:"Start only the named apps, not their dependencies."`
	Timeout   time.Duration `default:"2m" help:"How long to wait for each app to be ready."`
	Selection selection     `embed:""`
}

type stopCommand struct {
	Apps      []string  `arg:"" optional:"" help:"Apps declared in an ilot.yaml."`
	All       bool      `help:"Stop every app that the stack owns."`
	Selection selection `embed:""`
}

func (command *lsCommand) Run(ctx context.Context) error {
	executor, _, err := newExecutorFor(ctx, command.Selection.selection(), time.Minute, baseStack)
	if err != nil {
		return err
	}
	listing, err := executor.List(ctx)
	if err != nil {
		return err
	}
	checkoutsByStack, err := executor.StackCheckouts()
	if err != nil {
		return err
	}
	if command.JSON {
		return json.NewEncoder(os.Stdout).Encode(lsJSONOutput{Home: executor.Machine.HomeURL(), Listing: listing, CheckoutsByStack: checkoutsByStack})
	}
	fmt.Printf("every URL: %s\n\n", executor.Machine.HomeURL())
	table := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "STACK\tAPP\tSTATUS\tPORTS\tURL")
	hasUndeclared := false
	for _, app := range listing.Apps {
		var formattedPorts []string
		for _, portName := range slices.Sorted(maps.Keys(app.Ports)) {
			formattedPorts = append(formattedPorts, fmt.Sprintf("%s=%d", portName, app.Ports[portName]))
		}
		status := app.Status
		if app.Undeclared {
			status += ", undeclared"
			hasUndeclared = true
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", app.Stack, app.App, status, strings.Join(formattedPorts, " "), app.URL)
	}
	for _, externalService := range listing.ExternalServices {
		fmt.Fprintf(table, "external\t%s\t%s\t%d\t\n", externalService.Name, externalService.Status, externalService.Port)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if hasUndeclared {
		fmt.Println("\nno ilot.yaml declares an app marked undeclared: it keeps its ports until ilot stop <app>, in its stack, removes it")
	}
	if listing.Restore != "" {
		fmt.Printf("\napps that a stack owns are down: %s starts them again\n", listing.Restore)
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
	executor, _, err := newExecutorFor(ctx, command.Selection.selection(), time.Minute, baseStack)
	if err != nil {
		return err
	}
	if err := executor.Remove(ctx, command.Stack); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "removed stack %s\n", command.Stack)
	return nil
}

func (command *cleanCommand) Run(ctx context.Context) error {
	currentWorkspace, _, err := selectedWorkspace(command.Selection.Workspace)
	if err != nil {
		return err
	}
	currentWorkspace, store, err := withBindings(currentWorkspace)
	if err != nil {
		return err
	}
	executor, err := newExecutor(ctx, currentWorkspace, store, workspace.BaseStackName, time.Minute)
	if err != nil {
		return err
	}
	return cleanStacks(ctx, os.Stdout, currentWorkspace, store, executor, command.DryRun)
}

// cleanStacks keeps a stack whose worktree was moved with git worktree move: it binds the stack to the new folder.
// A dry run prints what a clean would do, and changes nothing.
func cleanStacks(ctx context.Context, output io.Writer, currentWorkspace workspace.Workspace, store state.Store, executor *engine.Executor, isDryRun bool) error {
	verb := func(done, planned string) string {
		if isDryRun {
			return "would " + planned
		}
		return done
	}
	stacksWithoutWorktrees, err := executor.StacksWithoutWorktrees()
	if err != nil {
		return err
	}
	for _, stackName := range stacksWithoutWorktrees {
		movedByRepo, err := currentWorkspace.MovedWorktrees(stackName)
		if err != nil {
			return err
		}
		for _, repoName := range slices.Sorted(maps.Keys(movedByRepo)) {
			if !isDryRun {
				if currentWorkspace, _, err = bindWorktree(ctx, store, currentWorkspace, movedByRepo[repoName], stackName); err != nil {
					return err
				}
			}
			fmt.Fprintf(output, "%s %s: its worktree of %s moved to %s\n", verb("kept", "keep"), stackName, repoName, movedByRepo[repoName].Dir)
		}
		if len(movedByRepo) > 0 {
			continue
		}
		current, err := store.Load()
		if err != nil {
			return err
		}
		var wantedApps []string
		for appName, app := range current.Stacks(currentWorkspace.Name)[stackName].Apps {
			if app.Wanted {
				wantedApps = append(wantedApps, appName)
			}
		}
		if !isDryRun {
			if err := executor.Remove(ctx, stackName); err != nil {
				return err
			}
		}
		fmt.Fprintf(output, "%s %s\n", verb("removed", "remove"), stackName)
		slices.Sort(wantedApps)
		for _, appName := range wantedApps {
			fmt.Fprintf(output, "  %s %s\n", verb("stopped", "stop"), appName)
		}
	}
	return nil
}

func (command *startCommand) Run(ctx context.Context) error {
	switch {
	case command.All && len(command.Apps) > 0:
		return errors.New("--all starts every app that a stack owns: name no app with it")
	case command.All && command.Selection.Stack != "":
		return errors.New("--all starts the apps of every stack: give no --stack with it")
	case command.All:
		executor, _, err := newExecutorFor(ctx, command.Selection, command.Timeout, baseStack)
		if err != nil {
			return err
		}
		return executor.StartAll(ctx)
	case len(command.Apps) == 0:
		executor, stackName, err := newExecutorFor(ctx, command.Selection, command.Timeout, stackOfCurrentFolder)
		if err != nil {
			return err
		}
		return executor.StartStack(ctx, stackName)
	}
	executor, stackName, err := newExecutorFor(ctx, command.Selection, command.Timeout, stackOfCurrentFolderWithBinding)
	if err != nil {
		return err
	}
	return executor.Start(ctx, stackName, command.Apps, engine.StartOptions{SkipDependencies: command.NoDeps})
}

func (command *stopCommand) Run(ctx context.Context) error {
	switch {
	case command.All && len(command.Apps) > 0:
		return errors.New("--all stops every app that the stack owns: name no app with it")
	case !command.All && len(command.Apps) == 0:
		return errors.New("name the apps to stop, or give --all to stop every app that the stack owns")
	}
	executor, stackName, err := newExecutorFor(ctx, command.Selection, time.Minute, stackOfCurrentFolder)
	if err != nil {
		return err
	}
	if command.All {
		return executor.StopStack(ctx, stackName)
	}
	return executor.Stop(ctx, stackName, command.Apps)
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

func newExecutorFor(ctx context.Context, chosen selection, readyTimeout time.Duration, choice stackChoice) (*engine.Executor, string, error) {
	currentWorkspace, worktree, err := selectedWorkspace(chosen.Workspace)
	if err != nil {
		return nil, "", err
	}
	currentWorkspace, store, err := withBindings(currentWorkspace)
	if err != nil {
		return nil, "", err
	}
	stackName := workspace.BaseStackName
	if choice != baseStack {
		currentWorkspace, stackName, err = chosen.stackOf(ctx, store, currentWorkspace, worktree, choice == stackOfCurrentFolderWithBinding)
		if err != nil {
			return nil, "", err
		}
	}
	executor, err := newExecutor(ctx, currentWorkspace, store, stackName, readyTimeout)
	return executor, stackName, err
}

// sourceFor is for a command that only reads: it records no binding.
func sourceFor(ctx context.Context, chosen selection) (engine.Source, string, error) {
	currentWorkspace, worktree, err := selectedWorkspace(chosen.Workspace)
	if err != nil {
		return engine.Source{}, "", err
	}
	currentWorkspace, store, err := withBindings(currentWorkspace)
	if err != nil {
		return engine.Source{}, "", err
	}
	currentWorkspace, stackName, err := chosen.stackOf(ctx, store, currentWorkspace, worktree, false)
	if err != nil {
		return engine.Source{}, "", err
	}
	source, err := newSource(ctx, currentWorkspace, store, stackName)
	return source, stackName, err
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

// newSource gives what a command of a stack reads: the state, the files of its checkouts and the login shell.
// A configuration that cannot be read is an error for a command that renders, and the Source keeps it for the others.
func newSource(ctx context.Context, currentWorkspace workspace.Workspace, store state.Store, stackName string) (engine.Source, error) {
	stackConfig, configErr := currentWorkspace.ConfigForStack(stackName)
	global := currentWorkspace.Global
	captureShell := sync.OnceValues(func() (map[string]string, error) {
		return shellenv.OfUser(global.ShellEnv.Args).Environment(ctx)
	})
	_, proxyPort, err := net.SplitHostPort(global.Proxy.Listen)
	if err != nil {
		return engine.Source{}, err
	}
	currentUser, err := user.Current()
	if err != nil {
		return engine.Source{}, fmt.Errorf("find the name of the current user, for the User template: %w", err)
	}
	ilotBinary, err := os.Executable()
	if err != nil {
		return engine.Source{}, fmt.Errorf("find the ilot binary, which the watcher of an app runs: %w", err)
	}
	store.OnLockWait = func(holder string) {
		sayWhoHoldsTheLock(holder)
		auditLog.Info("state lock wait", "workspace", currentWorkspace.Name, "stack", stackName, "holder", holder)
	}
	ilotEnv := map[string]string{}
	if value, isSet := os.LookupEnv("XDG_STATE_HOME"); isSet {
		ilotEnv["XDG_STATE_HOME"] = value
	}
	return engine.Source{
		Workspace:         currentWorkspace.Name,
		Root:              currentWorkspace.RootDir,
		Config:            stackConfig,
		ConfigErr:         configErr,
		Store:             store,
		CheckoutsForStack: currentWorkspace.CheckoutsForStack,
		ConfigForStack:    currentWorkspace.ConfigForStack,
		ReposNotCloned:    currentWorkspace.ReposNotCloned(),
		Settings:          currentWorkspace.Settings,
		Machine:           plan.Machine{StateDir: store.RootDir, Domain: global.Proxy.Domain, ProxyPort: proxyPort, Login: currentUser.Username, ShellEnv: nil, SessionEnv: nil, IlotBinary: ilotBinary, IlotEnv: ilotEnv},
		ShellEnv: func() (map[string]string, map[string]string, error) {
			environment, err := captureShell()
			if err != nil {
				return nil, nil, err
			}
			stable, session := shellenv.SplitSession(environment)
			return stable, session, nil
		},
	}, nil
}

func newExecutor(ctx context.Context, currentWorkspace workspace.Workspace, store state.Store, stackName string, readyTimeout time.Duration) (*engine.Executor, error) {
	source, err := newSource(ctx, currentWorkspace, store, stackName)
	if err != nil {
		return nil, err
	}
	global := currentWorkspace.Global
	logger := auditLog.With("workspace", currentWorkspace.Name, "stack", stackName)
	return &engine.Executor{
		Source:              source,
		Supervisor:          engine.AuditedSupervisor{ProcessSupervisor: newDaemonSupervisor(source.Machine.IlotBinary, store.RootDir, global.Proxy.Listen), Logger: logger},
		RunBuild:            runBuild,
		EnsureImage:         ensureImage,
		RemoveContainer:     removeContainer,
		IsPortFreeOnHost:    ports.IsFreeOnLoopback,
		IsExternalServiceUp: isListeningOnLocalhost,
		PortRange:           ports.Range{First: global.Ports.First, Last: global.Ports.Last},
		ReadyTimeout:        readyTimeout,
		ProgressInterval:    10 * time.Second,
		Output:              os.Stderr,
		Data:                os.Stdout,
		Logger:              logger,
	}, nil
}

// newDaemonSupervisor gives the daemon of a state folder, which it starts with the proxy address of the global file.
func newDaemonSupervisor(ilotBinary, stateDir, proxyListen string) *engine.DaemonSupervisor {
	daemonPaths := daemon.Paths{StateDir: stateDir}
	startDaemon := func() error {
		_, err := daemon.Launch{Binary: ilotBinary, Args: []string{"daemon", "run", "--state-dir", stateDir, "--proxy-listen", proxyListen}, Env: nil, LogFile: daemonPaths.LogFile()}.Start()
		return err
	}
	return &engine.DaemonSupervisor{Starter: daemon.Starter{Paths: daemonPaths, Spawn: startDaemon, Timeout: 0}, Version: versionOfThisBinary(), ProxyListen: proxyListen, Warnings: os.Stderr}
}

// The same shell as the daemon gives an app, so a build and a run command read the same syntax.
func runBuild(ctx context.Context, workingDir, shellCommand string, environ []string) error {
	command := exec.CommandContext(ctx, "/bin/sh", "-c", shellCommand)
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

func removeContainer(ctx context.Context, name string) error {
	output, err := exec.CommandContext(ctx, "docker", "rm", "--force", name).CombinedOutput()
	if err != nil && !strings.Contains(string(output), "No such container") {
		return fmt.Errorf("docker rm --force %s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return nil
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
	closeAuditLog := openAuditLog(commandLine.Command())
	err := commandLine.Run()
	if err != nil {
		auditLog.Warn("command failed", "error", err.Error())
	}
	closeAuditLog()
	commandLine.FatalIfErrorf(err)
}
