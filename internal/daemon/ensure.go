package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const (
	defaultStartTimeout = 10 * time.Second
	startPollInterval   = 50 * time.Millisecond
	// A daemon that started while the old one still held the lock has exited. The next one gets the lock.
	respawnInterval = time.Second
)

// Launch describes the daemon process: the ilot binary with the arguments of its daemon command.
type Launch struct {
	Binary string
	Args   []string
	// Nil gives the daemon the environment of the caller.
	Env     []string
	LogFile string
}

// Start runs the daemon in its own session, so that it outlives the terminal of the caller.
func (launch Launch) Start() (pid int, err error) {
	if err := os.MkdirAll(filepath.Dir(launch.LogFile), 0o700); err != nil {
		return 0, err
	}
	// The daemon writes its records by itself. This handle catches what it cannot: a panic.
	output, err := os.OpenFile(launch.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, daemonLogPerm)
	if err != nil {
		return 0, err
	}
	defer output.Close()
	if err := output.Chmod(daemonLogPerm); err != nil {
		return 0, err
	}
	command := exec.Command(launch.Binary, launch.Args...)
	command.Env = launch.Env
	// A daemon in the folder of a worktree would block the removal of that folder.
	command.Dir = "/"
	command.Stdout, command.Stderr = output, output
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return 0, fmt.Errorf("start the ilot daemon: %w", err)
	}
	// A daemon that loses the race for the lock exits at once, and must not stay as a zombie of a long caller.
	go func() { _ = command.Wait() }()
	return command.Process.Pid, nil
}

type Starter struct {
	Paths Paths
	// Spawn starts one daemon process and does not wait for it.
	Spawn   func() error
	Timeout time.Duration
}

// Ensure returns a client of a daemon that answers, and starts the daemon when nothing listens.
//
// It never removes a file and never tests the lock. Each new daemon tests the lock by itself,
// so two callers at the same time give two processes, and the one that loses exits.
func (starter Starter) Ensure(ctx context.Context) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client := NewClient(starter.Paths.Socket())
	waitReason, err := whyNotUsable(ctx, client)
	if err != nil {
		return nil, err
	}
	if waitReason == nil {
		return client, nil
	}
	if err := starter.Spawn(); err != nil {
		return nil, err
	}
	lastSpawn := time.Now()

	timeout := cmp.Or(starter.Timeout, defaultStartTimeout)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		waitReason, err := whyNotUsable(ctx, client)
		if err != nil {
			return nil, err
		}
		if waitReason == nil {
			return client, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("%w after %s: read %s", waitReason, timeout, starter.Paths.LogFile())
		case <-time.After(startPollInterval):
		}
		if time.Since(lastSpawn) >= respawnInterval {
			if err := starter.Spawn(); err != nil {
				return nil, err
			}
			lastSpawn = time.Now()
		}
	}
}

// whyNotUsable gives waitReason when a new daemon can still come, and err when a wait cannot help.
// A daemon that shuts down still answers for a while, then closes its socket under the callers.
func whyNotUsable(ctx context.Context, client *Client) (waitReason, err error) {
	status, err := client.Status(ctx)
	switch {
	case err == nil && status.ShuttingDown:
		return fmt.Errorf("%w: the daemon %d still stops its processes", ErrShuttingDown, status.PID), nil
	case err == nil:
		return nil, nil
	case errors.Is(err, ErrDaemonDown):
		return err, nil
	case errors.Is(err, errConnectionBroken):
		return fmt.Errorf("%w: %w", ErrDaemonDown, err), nil
	default:
		return nil, err
	}
}
