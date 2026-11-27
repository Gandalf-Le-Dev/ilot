package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

const (
	defaultProbeInterval = time.Second
	defaultProbeTimeout  = 5 * time.Second
)

// Probe tells when a process is ready. A command wins over a path, and a path wins over a bare port.
type Probe struct {
	Host    string `json:"host,omitempty"`
	Port    int    `json:"port,omitzero"`
	Path    string `json:"path,omitempty"`
	Command string `json:"command,omitempty"`
}

func (probe Probe) Validate() error {
	if probe.Command == "" && probe.Port == 0 {
		return errors.New("the readiness probe has no command and no port")
	}
	return nil
}

func (probe Probe) address() string {
	return net.JoinHostPort(cmp.Or(probe.Host, loopbackName), strconv.Itoa(probe.Port))
}

// The proxy settings of the environment must not send a loopback probe to another machine.
var probeHTTPClient = &http.Client{
	Transport: &http.Transport{Proxy: nil, DialContext: dialLoopback, DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func (probe Probe) check(ctx context.Context, spec Spec) error {
	switch {
	case probe.Command != "":
		return shellCommand(ctx, spec, probe.Command).Run()
	case probe.Path != "":
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+probe.address()+probe.Path, nil)
		if err != nil {
			return err
		}
		response, err := probeHTTPClient.Do(request)
		if err != nil {
			return err
		}
		response.Body.Close()
		if response.StatusCode >= http.StatusBadRequest {
			return fmt.Errorf("GET %s gives status %d", request.URL, response.StatusCode)
		}
		return nil
	default:
		connection, err := dialLoopback(ctx, "tcp", probe.address())
		if err != nil {
			return err
		}
		return connection.Close()
	}
}

// probeUntilReady never signals the process: a slow start is not a failure.
func (supervisor *Supervisor) probeUntilReady(name string, spec Spec, probed *run) {
	ticker := time.NewTicker(supervisor.probeInterval)
	defer ticker.Stop()
	for {
		attempt, cancel := context.WithTimeout(probed.dead, supervisor.probeTimeout)
		err := spec.Ready.check(attempt, spec)
		cancel()
		// A probe can pass after the exit, for example on a port that a grandchild holds.
		if probed.dead.Err() != nil {
			return
		}
		if err == nil {
			supervisor.markReady(name, probed)
			return
		}
		select {
		case <-probed.dead.Done():
			return
		case <-ticker.C:
		}
	}
}

func (supervisor *Supervisor) readyIfStillAlive(name string, started *run) {
	select {
	case <-started.dead.Done():
	case <-supervisor.afterAliveWindow(supervisor.aliveWindow):
		// Both can be true at once, and the select then picks at random.
		if started.dead.Err() == nil {
			supervisor.markReady(name, started)
		}
	}
}

func (supervisor *Supervisor) markReady(name string, target *run) {
	supervisor.mu.Lock()
	if target.state == StateStarting {
		target.state = StateReady
	}
	supervisor.mu.Unlock()
	close(target.ready)
	supervisor.logger.Info("process ready", "process", name, "pid", target.pid)
}

type ExitedError struct {
	Name     string
	ExitCode int
}

func (exitedError *ExitedError) Error() string {
	return fmt.Sprintf("%s exited with code %d", exitedError.Name, exitedError.ExitCode)
}

// WaitReady returns the error of ctx when the wait ends first. The process then still runs.
func (supervisor *Supervisor) WaitReady(ctx context.Context, name string) error {
	supervisor.mu.Lock()
	target, isKnown := supervisor.entries[name]
	var waited *run
	if isKnown {
		waited = target.run
	}
	supervisor.mu.Unlock()
	if !isKnown {
		return fmt.Errorf("%s: %w", name, ErrUnknownProcess)
	}
	if waited == nil {
		return &ExitedError{Name: name, ExitCode: exitCodeNotStarted}
	}
	select {
	case <-waited.ready:
	case <-waited.exited.Done():
	case <-ctx.Done():
		return ctx.Err()
	}
	// A process that was ready and then exited is of no use to the caller.
	if waited.exited.Err() != nil {
		supervisor.mu.Lock()
		defer supervisor.mu.Unlock()
		return &ExitedError{Name: name, ExitCode: waited.exitCode}
	}
	return nil
}
