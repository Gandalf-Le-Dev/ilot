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
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/alecthomas/kong"

	"dev/internal/checkouts"
	"dev/internal/devctl"
	"dev/internal/logs"
	"dev/internal/ports"
	"dev/internal/processcompose"
	"dev/internal/shellenv"
	"dev/internal/stack"
	"dev/internal/state"
)

const (
	devboxMarkerFile       = "/etc/devhost.env"
	traefikRoutesMountPath = "/etc/traefik/routes"
)

type cli struct {
	Version kong.VersionFlag `short:"v" help:"Print the version of dev and exit."`
	Skill   skillFlag        `help:"Print the dev skill for AI agents and exit."`

	Ls        lsCommand        `cmd:"" help:"List the apps of every stack and the external services."`
	Apps      appsCommand      `cmd:"" help:"List the apps and the external services that the dev.yaml files declare for the stack of the current worktree, with the dependencies of each app."`
	Start     startCommand     `cmd:"" help:"Build and start apps in the stack of the current worktree, after their dependencies."`
	Stop      stopCommand      `cmd:"" help:"Stop apps in the stack of the current worktree."`
	Boot      bootCommand      `cmd:"" help:"Start the autostart apps and the apps that ran before devbox restarted. Does nothing when dev already runs."`
	Restart   restartCommand   `cmd:"" help:"Stop every app of every stack, start dev again with a fresh environment from the shell files, then start the apps that ran and the autostart apps."`
	Env       envCommand       `cmd:"" help:"Print the environment of an app in the stack of the current worktree, with where each value comes from."`
	Logs      logsCommand      `cmd:"" help:"Print the log of an app in the stack of the current worktree. A shared app has one log for every stack."`
	Rm        rmCommand        `cmd:"" help:"Stop the apps of a stack and delete its state, logs, routes and Temporal namespace."`
	Clean     cleanCommand     `cmd:"" help:"Remove every stack whose worktrees are all deleted."`
	Check     checkCommand     `cmd:"" help:"Check a dev.yaml with the dev.yaml of the other repos: config rules and every template."`
	Rebuild   rebuildCommand   `cmd:"" help:"Build dev from the platform main checkout again, with the full build output."`
	Bootstrap bootstrapCommand `cmd:"" help:"Set up the Artifactory token for npm, jf and docker, the GPG loopback lines and the agent unlock. Safe to run again."`
}

type envCommand struct {
	App string `arg:"" help:"App declared in a dev.yaml."`
}

const unrecordedEnvironmentNote = "note: dev started before it fixed the environment of the apps: they have the environment of the shell that started it. Run dev restart."

func (command *envCommand) Run(ctx context.Context) error {
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
	if err := table.Flush(); err != nil {
		return err
	}
	isUnrecorded, err := controller.RunsWithoutARecordedEnvironment(ctx)
	if isUnrecorded {
		fmt.Fprintln(os.Stderr, unrecordedEnvironmentNote)
	}
	return err
}

type logsCommand struct {
	App    string `arg:"" help:"App declared in devbox/dev.yaml."`
	Follow bool   `short:"f" help:"Keep printing new lines."`
	Lines  int    `short:"n" default:"100" help:"How many of the last lines to print first."`
	JSON   bool   `help:"Print the app's own log lines as they are, for tools and agents."`
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
	Checkout string        `required:"" type:"existingdir" help:"Main checkout of the platform repo, whose devbox/dev.yaml is read."`
	Timeout  time.Duration `default:"5m" help:"How long to wait for each app to be ready."`
}

type restartCommand struct {
	Timeout time.Duration `default:"5m" help:"How long to wait for each app to be ready."`
}

func (command *restartCommand) Run(ctx context.Context) error {
	controller, mainCheckout, err := newControllerForCurrentWorktree(command.Timeout, true)
	if err != nil {
		return err
	}
	return controller.Restart(ctx, mainCheckout)
}

type lsCommand struct {
	JSON bool `help:"Print the apps, the external services and the checkout of each repo per stack as one JSON object, for tools and agents."`
}

type lsJSONOutput struct {
	Home string `json:"home"`
	devctl.Listing
	CheckoutsByStack map[string]map[string]checkouts.Checkout `json:"checkouts"`
}

type rmCommand struct {
	Stack string `arg:"" help:"Stack to remove, as dev ls shows it."`
}

type cleanCommand struct{}

type startCommand struct {
	Apps    []string      `arg:"" help:"Apps declared in devbox/dev.yaml."`
	NoDeps  bool          `help:"Start only the named apps, not their dependencies."`
	Timeout time.Duration `default:"2m" help:"How long to wait for each app to be ready."`
}

type stopCommand struct {
	Apps []string `arg:"" help:"Apps declared in devbox/dev.yaml."`
}

func (command *bootCommand) Run(ctx context.Context) error {
	installAgentSkillOrWarn(os.UserHomeDir, os.Stderr)
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
	fmt.Printf("home: %s\n\n", controller.HomeURL())
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
	return controller.Start(ctx, worktree, command.Apps, devctl.StartOptions{SkipDependencies: command.NoDeps})
}

func (command *stopCommand) Run(ctx context.Context) error {
	controller, worktree, err := newControllerForCurrentWorktree(time.Minute, false)
	if err != nil {
		return err
	}
	return controller.Stop(ctx, worktree, command.Apps)
}

func newControllerForCurrentWorktree(readyTimeout time.Duration, isCrossStack bool) (*devctl.Controller, stack.Worktree, error) {
	currentDir, err := os.Getwd()
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

func newController(currentWorkspace workspace, worktree stack.Worktree, readyTimeout time.Duration) (*devctl.Controller, error) {
	if _, err := os.Stat(devboxMarkerFile); err != nil {
		return nil, errors.New("dev runs inside devbox only for now: ssh into devbox and run it there")
	}
	config, err := currentWorkspace.configForStack(worktree.Name)
	if err != nil {
		return nil, err
	}
	processComposeBinary, err := exec.LookPath("process-compose")
	if err != nil {
		return nil, errors.New("process-compose is not on PATH: install the version pinned in devbox/README.md")
	}
	routesDir, err := findRoutesDir()
	if err != nil {
		return nil, err
	}
	stateDir := filepath.Join(cmp.Or(os.Getenv("XDG_STATE_HOME"), filepath.Join(currentWorkspace.homeDir, ".local", "state")), "dev")
	shellEnv := sync.OnceValues(func() (map[string]string, error) { return shellenv.Base(context.Background()) })
	controller := &devctl.Controller{
		Config:                  config,
		Store:                   state.Store{RootDir: stateDir},
		Supervisor:              processcompose.Client{Binary: processComposeBinary, StateDir: filepath.Join(stateDir, "process-compose"), Env: shellEnv},
		RunBuild:                runBuild,
		EnsureImage:             ensureImage,
		IsPortFreeOnHost:        ports.IsFreeOnHost,
		IsExternalServiceUp:     isListeningOnLocalhost,
		CheckoutsForStack:       currentWorkspace.checkoutsForStack,
		ConfigForStack:          currentWorkspace.configForStack,
		UsesMise:                hasMiseConfig,
		ReposNotCloned:          currentWorkspace.reposNotCloned(),
		HostPath:                hostPathFor(currentWorkspace.homeDir, os.Getenv("LC_DEVBOX_LOGIN")),
		EnsureTemporalNamespace: ensureTemporalNamespace(temporalAddress(config)),
		DeleteTemporalNamespace: deleteTemporalNamespace(temporalAddress(config)),
		Personal:                currentWorkspace.settings,
		KeyringFile:             filepath.Join(currentWorkspace.homeDir, ".keyring"),
		ShellEnv:                shellEnv,
		Login:                   os.Getenv("LC_DEVBOX_LOGIN"),
		RoutesDir:               routesDir,
		BackendHost:             "devbox",
		ProxyPort:               cmp.Or(os.Getenv("DEVPROXY_PORT"), os.Getenv("DEVHOST_PORT_7")),
		ReadyTimeout:            readyTimeout,
		ProgressInterval:        10 * time.Second,
		Output:                  os.Stdout,
	}
	return controller, nil
}

func findRoutesDir() (string, error) {
	if routesDir := os.Getenv("DEV_ROUTES_DIR"); routesDir != "" {
		return routesDir, nil
	}
	proxyContainer := "devproxy_" + os.Getenv("LC_DEVBOX_LOGIN")
	mountsJSON, err := exec.Command("docker", "inspect", "--format", "{{json .Mounts}}", proxyContainer).Output()
	if err != nil {
		return "", fmt.Errorf("find the routes folder of %s (set DEV_ROUTES_DIR to skip this): %w", proxyContainer, err)
	}
	return routesDirFromMounts(mountsJSON)
}

func routesDirFromMounts(mountsJSON []byte) (string, error) {
	var mounts []struct {
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	}
	if err := json.Unmarshal(mountsJSON, &mounts); err != nil {
		return "", fmt.Errorf("read proxy mounts: %w", err)
	}
	for _, mount := range mounts {
		if mount.Destination == traefikRoutesMountPath {
			return mount.Source, nil
		}
	}
	return "", fmt.Errorf("the proxy container mounts nothing at %s", traefikRoutesMountPath)
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
	runCheckoutBinaryInstead(context.Background())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var commandLineArguments cli
	commandLine := kong.Parse(&commandLineArguments, kong.Name("dev"), kong.Description("Run the devbox apps of each worktree behind the dev proxy.\n\nAgents: if the dev skill is not in your context, run dev --skill."), kong.BindTo(ctx, (*context.Context)(nil)), kong.Vars{"version": versionOfThisBinary(), "artifactory_host": defaultArtifactoryHost})
	commandLine.FatalIfErrorf(commandLine.Run())
}
