package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/engine"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

type daemonStatusCommand struct{}

type daemonStopCommand struct{}

type daemonRestartCommand struct {
	Timeout time.Duration `default:"5m" help:"How long to wait for each app to be ready."`
}

// daemonOfThisMachine gives the daemon with no workspace: it is the same for every workspace of the user.
func daemonOfThisMachine() (*engine.DaemonSupervisor, config.Global, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, config.Global{}, err
	}
	global, err := config.LoadGlobal(homeDir)
	if err != nil {
		return nil, config.Global{}, err
	}
	ilotBinary, err := os.Executable()
	if err != nil {
		return nil, config.Global{}, err
	}
	return newDaemonSupervisor(ilotBinary, stateDirOf(homeDir), global.Proxy.Listen), global, nil
}

func (daemonStatusCommand) Run(ctx context.Context) error {
	supervisor, global, err := daemonOfThisMachine()
	if err != nil {
		return err
	}
	client := daemon.NewClient(supervisor.Starter.Paths.Socket())
	status, err := client.Status(ctx)
	if errors.Is(err, daemon.ErrDaemonDown) {
		fmt.Println("the ilot daemon does not run: ilot starts it when a command needs it")
		return nil
	}
	if err != nil {
		return err
	}
	processes, err := client.Processes(ctx)
	if err != nil {
		return err
	}
	printDaemonStatus(os.Stdout, status, len(processes), time.Now())
	for _, warning := range engine.DaemonWarnings(status, versionOfThisBinary(), global.Proxy.Listen) {
		fmt.Fprintln(os.Stderr, "warning: "+warning)
	}
	return nil
}

func printDaemonStatus(output io.Writer, status daemon.Status, processCount int, now time.Time) {
	fmt.Fprintf(output, "version: %s\n", status.Version)
	fmt.Fprintf(output, "pid: %d\n", status.PID)
	fmt.Fprintf(output, "started: %s (%s ago)\n", status.StartedAt.Local().Format(time.DateTime), now.Sub(status.StartedAt).Round(time.Second))
	if status.ProxyError != "" {
		fmt.Fprintf(output, "proxy: does not listen: %s\n", status.ProxyError)
	} else {
		fmt.Fprintf(output, "proxy: %s\n", status.ProxyAddress)
	}
	fmt.Fprintf(output, "processes: %d\n", processCount)
	if status.ShuttingDown {
		fmt.Fprintln(output, "state: shutting down")
	}
}

func (daemonStopCommand) Run(ctx context.Context) error {
	supervisor, _, err := daemonOfThisMachine()
	if err != nil {
		return err
	}
	if !supervisor.IsUp(ctx) {
		fmt.Fprintln(os.Stderr, "the ilot daemon does not run")
		return nil
	}
	fmt.Fprintln(os.Stderr, "stopping every app of every stack, and the ilot daemon")
	if err := (engine.AuditedSupervisor{ProcessSupervisor: supervisor, Logger: auditLog}).Shutdown(ctx); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "the ilot daemon stopped. The stacks keep their apps: ilot start --all starts them again")
	return nil
}

// The shutdown stops the apps of every workspace, so the restart starts again the members of every workspace that the state records, from any folder.
func (command *daemonRestartCommand) Run(ctx context.Context) error {
	supervisor, _, err := daemonOfThisMachine()
	if err != nil {
		return err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	current, err := state.Store{RootDir: stateDirOf(homeDir)}.Load()
	if err != nil {
		return err
	}
	startAllOf := func(ctx context.Context, workspaceName string) error {
		executor, _, err := newExecutorFor(ctx, selection{Workspace: workspaceName, Stack: ""}, command.Timeout, baseStack)
		if err != nil {
			return err
		}
		return executor.StartAll(ctx)
	}
	return restartDaemon(ctx, os.Stderr, engine.AuditedSupervisor{ProcessSupervisor: supervisor, Logger: auditLog}, workspacesWithMembers(current), startAllOf)
}

type daemonRestarter interface {
	IsUp(ctx context.Context) bool
	Shutdown(ctx context.Context) error
	Up(ctx context.Context) error
}

// restartDaemon goes on after a workspace that fails, so that one broken ilot.yaml does not keep the other workspaces down.
func restartDaemon(ctx context.Context, output io.Writer, supervisor daemonRestarter, workspaceNames []string, startAllOf func(context.Context, string) error) error {
	if supervisor.IsUp(ctx) {
		fmt.Fprintln(output, "stopping every app of every stack, and the ilot daemon")
	}
	if err := supervisor.Shutdown(ctx); err != nil {
		return err
	}
	if err := supervisor.Up(ctx); err != nil {
		return err
	}
	if len(workspaceNames) == 0 {
		fmt.Fprintln(output, "the new ilot daemon runs. No stack owns an app, so it started none")
		return nil
	}
	var problems []error
	for _, workspaceName := range workspaceNames {
		fmt.Fprintf(output, "starting the apps of the workspace %s again\n", workspaceName)
		if err := startAllOf(ctx, workspaceName); err != nil {
			problems = append(problems, fmt.Errorf("the apps of the workspace %s stay down: %w\nFix the cause, then run: ilot start --all --workspace %s", workspaceName, err, workspaceName))
		}
	}
	return errors.Join(problems...)
}

func workspacesWithMembers(current state.State) []string {
	var names []string
	for _, workspaceName := range slices.Sorted(maps.Keys(current.Workspaces)) {
		known := current.Workspaces[workspaceName]
		if known.Root == "" {
			continue
		}
		hasMember := false
		for _, stack := range known.Stacks {
			for _, app := range stack.Apps {
				hasMember = hasMember || app.Wanted
			}
		}
		if hasMember {
			names = append(names, workspaceName)
		}
	}
	return names
}
