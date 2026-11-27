package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"time"
)

// ErrDaemonDown means that nothing listens on the socket. It does not prove that no daemon lives: the lock file does.
var ErrDaemonDown = errors.New("the ilot daemon does not answer")

var errConnectionBroken = errors.New("the daemon closed the connection")

const (
	readyPollInterval = 200 * time.Millisecond
	// Longer than the time that the daemon gives its API calls to end.
	shutdownExitWait     = 2 * apiDrainTimeout
	shutdownPollInterval = 10 * time.Millisecond
)

type APIError struct {
	StatusCode int
	Message    string
}

func (apiError *APIError) Error() string {
	return apiError.Message
}

func (apiError *APIError) Unwrap() error {
	switch apiError.StatusCode {
	case http.StatusNotFound:
		return ErrUnknownProcess
	case http.StatusServiceUnavailable:
		return ErrShuttingDown
	default:
		return nil
	}
}

type NotReadyError struct {
	Name  string
	State State
	cause error
}

func (notReadyError *NotReadyError) Error() string {
	return fmt.Sprintf("%s is not ready (%s)", notReadyError.Name, notReadyError.State)
}

func (notReadyError *NotReadyError) Unwrap() error {
	return notReadyError.cause
}

type Client struct {
	socketPath string
	http       *http.Client
}

func NewClient(socketPath string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		// A kept connection would outlive a restart of the daemon.
		DisableKeepAlives: true,
	}
	return &Client{socketPath: socketPath, http: &http.Client{Transport: transport}}
}

func (client *Client) Status(ctx context.Context) (Status, error) {
	var status Status
	err := client.call(ctx, http.MethodGet, "/v1/status", nil, &status)
	return status, err
}

func (client *Client) Processes(ctx context.Context) ([]Process, error) {
	var list processList
	err := client.call(ctx, http.MethodGet, "/v1/processes", nil, &list)
	return list.Processes, err
}

func (client *Client) Put(ctx context.Context, name string, spec Spec) (Process, error) {
	var process Process
	err := client.call(ctx, http.MethodPut, "/v1/processes/"+url.PathEscape(name), spec, &process)
	return process, err
}

func (client *Client) Restart(ctx context.Context, name string) (Process, error) {
	var process Process
	err := client.call(ctx, http.MethodPost, "/v1/processes/"+url.PathEscape(name)+"/restart", nil, &process)
	return process, err
}

func (client *Client) Delete(ctx context.Context, name string) error {
	return client.call(ctx, http.MethodDelete, "/v1/processes/"+url.PathEscape(name), nil, nil)
}

func (client *Client) SetRoutes(ctx context.Context, routes []Route) error {
	return client.call(ctx, http.MethodPut, "/v1/routes", routeList{Routes: routes}, nil)
}

// Shutdown returns after every process stopped and the daemon removed its socket file.
// The daemon removes that file as its last act before it frees the lock, so the next start does not meet the old daemon.
func (client *Client) Shutdown(ctx context.Context) error {
	if err := client.call(ctx, http.MethodPost, "/v1/shutdown", nil, nil); err != nil {
		return err
	}
	deadline := time.NewTimer(shutdownExitWait)
	defer deadline.Stop()
	for {
		if _, err := os.Lstat(client.socketPath); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("the ilot daemon stopped its processes, but it still holds %s after %s", client.socketPath, shutdownExitWait)
		case <-time.After(shutdownPollInterval):
		}
	}
}

// WaitReady returns a NotReadyError when ctx ends first. The process then still runs.
func (client *Client) WaitReady(ctx context.Context, name string) error {
	state := StateStarting
	for {
		processes, err := client.Processes(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return &NotReadyError{Name: name, State: state, cause: ctx.Err()}
			}
			return err
		}
		process, isKnown := findProcess(processes, name)
		if !isKnown {
			return fmt.Errorf("%s: %w", name, ErrUnknownProcess)
		}
		state = process.State
		switch state {
		case StateReady:
			return nil
		case StateExited:
			return &ExitedError{Name: name, ExitCode: process.ExitCode}
		}
		select {
		case <-ctx.Done():
			return &NotReadyError{Name: name, State: state, cause: ctx.Err()}
		case <-time.After(readyPollInterval):
		}
	}
}

func findProcess(processes []Process, name string) (Process, bool) {
	for _, process := range processes {
		if process.Name == name {
			return process, true
		}
	}
	return Process{}, false
}

// A daemon that exits closes its listener under the calls that just connected.
func isBrokenConnection(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

func (client *Client) call(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://ilot"+path, body)
	if err != nil {
		return err
	}
	response, err := client.http.Do(request)
	if err != nil {
		// A cancelled call says nothing about the daemon, so it must not look like a dead one.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return fmt.Errorf("%w on %s", ErrDaemonDown, client.socketPath)
		}
		if isBrokenConnection(err) {
			return fmt.Errorf("call the ilot daemon: %w: %w", errConnectionBroken, err)
		}
		return fmt.Errorf("call the ilot daemon: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		var failure errorBody
		if err := json.NewDecoder(response.Body).Decode(&failure); err != nil || failure.Error == "" {
			failure.Error = fmt.Sprintf("the ilot daemon answers %s to %s %s", response.Status, method, path)
		}
		return &APIError{StatusCode: response.StatusCode, Message: failure.Error}
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}
