package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/engine"
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
	if err := supervisor.Shutdown(ctx); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "the ilot daemon stopped. The stacks keep their apps: ilot start --all starts them again")
	return nil
}

// Outside a workspace, the restart starts no app: only the files of a workspace say how to start its apps.
func (command *daemonRestartCommand) Run(ctx context.Context) error {
	executor, _, findErr := newExecutorFor(ctx, selection{Workspace: "", Stack: ""}, command.Timeout, baseStack)
	if findErr == nil {
		return executor.RestartDaemon(ctx)
	}
	supervisor, _, err := daemonOfThisMachine()
	if err != nil {
		return err
	}
	if supervisor.IsUp(ctx) {
		fmt.Fprintln(os.Stderr, "stopping every app of every stack, and the ilot daemon")
	}
	if err := supervisor.Shutdown(ctx); err != nil {
		return err
	}
	if err := supervisor.Up(ctx); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "the new ilot daemon runs, and it started no app, because the current folder gives no workspace: %v\nRun ilot start --all in a repo of each workspace to start its apps again\n", findErr)
	return nil
}
