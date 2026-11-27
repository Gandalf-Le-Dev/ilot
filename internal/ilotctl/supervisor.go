package ilotctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
)

type ProcessSpec struct {
	Name string
	daemon.Spec
}

type ProcessSupervisor interface {
	// Apply starts each process that is new or that changed, and removes each process that specs does not name.
	// It leaves an ended process as it is: only Restart starts it again.
	Apply(ctx context.Context, specs []ProcessSpec) error
	Restart(ctx context.Context, processName string) error
	// Processes is empty when no daemon runs. It never starts one.
	Processes(ctx context.Context) (map[string]daemon.Process, error)
	// WaitReady returns a *daemon.ExitedError for an ended process, and a *daemon.NotReadyError after the timeout.
	WaitReady(ctx context.Context, processName string, timeout time.Duration) error
	IsUp(ctx context.Context) bool
	RestartCommand(processName string) string
	// SetRoutes replaces the route table of the proxy. It does nothing when no daemon runs.
	SetRoutes(ctx context.Context, routes []daemon.Route) error
	Shutdown(ctx context.Context) error
}

// DaemonSupervisor runs the processes under the ilot daemon, which it starts on demand.
type DaemonSupervisor struct {
	Starter daemon.Starter
	// The version of this ilot, and the proxy address that it starts a daemon with. A daemon that differs gets a warning.
	Version     string
	ProxyListen string
	Warnings    io.Writer

	warnOnce sync.Once
}

func (supervisor *DaemonSupervisor) client() *daemon.Client {
	return daemon.NewClient(supervisor.Starter.Paths.Socket())
}

func (supervisor *DaemonSupervisor) ensureDaemon(ctx context.Context) (*daemon.Client, error) {
	client, err := supervisor.Starter.Ensure(ctx)
	if err != nil {
		return nil, withRetryHint(err)
	}
	supervisor.warnAboutDaemon(ctx, client)
	return client, nil
}

// One time per command: a command makes several calls, and each would repeat the same lines.
func (supervisor *DaemonSupervisor) warnAboutDaemon(ctx context.Context, client *daemon.Client) {
	if supervisor.Warnings == nil {
		return
	}
	status, err := client.Status(ctx)
	if err != nil {
		return
	}
	supervisor.warnOnce.Do(func() {
		for _, warning := range daemonWarnings(status, supervisor.Version, supervisor.ProxyListen) {
			fmt.Fprintln(supervisor.Warnings, "warning: "+warning)
		}
	})
}

func daemonWarnings(status daemon.Status, version, proxyListen string) []string {
	var warnings []string
	if status.Version != version {
		warnings = append(warnings, fmt.Sprintf("the ilot daemon has the version %q and this ilot has the version %q: run ilot restart to start the daemon of this version", status.Version, version))
	}
	switch {
	case status.ProxyError != "":
		warnings = append(warnings, fmt.Sprintf("%s: the apps run, but their URLs do not answer. Free the address or set ILOT_PROXY_LISTEN to another one, then run ilot restart", status.ProxyError))
	case status.ProxyAddress != proxyListen:
		warnings = append(warnings, fmt.Sprintf("the proxy of the ilot daemon listens on %q, and this ilot expects %q: the URLs that ilot prints do not answer. Run ilot restart", status.ProxyAddress, proxyListen))
	}
	return warnings
}

// A daemon that shuts down refuses a start. The next command starts a new daemon.
func withRetryHint(err error) error {
	if errors.Is(err, daemon.ErrShuttingDown) {
		return fmt.Errorf("%w: run the command again", err)
	}
	return err
}

func (supervisor *DaemonSupervisor) Apply(ctx context.Context, specs []ProcessSpec) error {
	if len(specs) == 0 {
		running, err := supervisor.Processes(ctx)
		if err != nil || len(running) == 0 {
			return err
		}
	}
	client, err := supervisor.ensureDaemon(ctx)
	if err != nil {
		return err
	}
	known, err := supervisor.Processes(ctx)
	if err != nil {
		return err
	}
	isWanted := map[string]bool{}
	for _, spec := range specs {
		isWanted[spec.Name] = true
	}
	// The removals come first: a process that goes away can hold what a new one needs.
	for _, name := range slices.Sorted(maps.Keys(known)) {
		if isWanted[name] {
			continue
		}
		if err := client.Delete(ctx, name); err != nil {
			return withRetryHint(err)
		}
	}
	for _, spec := range specs {
		if process, isKnown := known[spec.Name]; isKnown && process.Hash == spec.Hash() {
			continue
		}
		if _, err := client.Put(ctx, spec.Name, spec.Spec); err != nil {
			return withRetryHint(fmt.Errorf("start %s: %w", spec.Name, err))
		}
	}
	return nil
}

func (supervisor *DaemonSupervisor) Restart(ctx context.Context, processName string) error {
	_, err := supervisor.client().Restart(ctx, processName)
	return withRetryHint(err)
}

func (supervisor *DaemonSupervisor) Processes(ctx context.Context) (map[string]daemon.Process, error) {
	processes, err := supervisor.client().Processes(ctx)
	if errors.Is(err, daemon.ErrDaemonDown) {
		return map[string]daemon.Process{}, nil
	}
	if err != nil {
		return nil, err
	}
	supervisor.warnAboutDaemon(ctx, supervisor.client())
	processByName := make(map[string]daemon.Process, len(processes))
	for _, process := range processes {
		processByName[process.Name] = process
	}
	return processByName, nil
}

func (supervisor *DaemonSupervisor) WaitReady(ctx context.Context, processName string, timeout time.Duration) error {
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := supervisor.client().WaitReady(waitContext, processName)
	if ctx.Err() != nil {
		// The caller gave up. That says nothing about the process.
		return ctx.Err()
	}
	if _, isNotReady := errors.AsType[*daemon.NotReadyError](err); isNotReady {
		return fmt.Errorf("%w after %s", err, timeout)
	}
	return err
}

func (supervisor *DaemonSupervisor) IsUp(ctx context.Context) bool {
	_, err := supervisor.client().Status(ctx)
	return err == nil
}

// The watcher of an app runs this command. It talks to the API of the daemon, so that it needs no state of ilot.
func (supervisor *DaemonSupervisor) RestartCommand(processName string) string {
	socket := supervisor.Starter.Paths.Socket()
	return "curl --silent --show-error --fail --unix-socket " + shellQuote(socket) + " -X POST " + shellQuote("http://ilot/v1/processes/"+processName+"/restart")
}

func (supervisor *DaemonSupervisor) SetRoutes(ctx context.Context, routes []daemon.Route) error {
	err := supervisor.client().SetRoutes(ctx, routes)
	if errors.Is(err, daemon.ErrDaemonDown) {
		return nil
	}
	return err
}

func (supervisor *DaemonSupervisor) Shutdown(ctx context.Context) error {
	err := supervisor.client().Shutdown(ctx)
	if errors.Is(err, daemon.ErrDaemonDown) {
		return nil
	}
	return err
}

func shellQuote(word string) string {
	return "'" + strings.ReplaceAll(word, "'", `'"'"'`) + "'"
}

func environ(valueByKey map[string]string) []string {
	pairs := make([]string, 0, len(valueByKey))
	for _, key := range slices.Sorted(maps.Keys(valueByKey)) {
		pairs = append(pairs, key+"="+valueByKey[key])
	}
	return pairs
}
