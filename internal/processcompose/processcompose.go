package processcompose

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type ReadyProbe struct {
	Host    string
	Port    int
	Path    string
	Command string
}

type ProcessSpec struct {
	Name       string
	Command    string
	WorkingDir string
	Env        map[string]string
	LogFile    string
	Ready      ReadyProbe
	// Runs instead of SIGTERM when the process stops.
	ShutdownCommand string
}

type ProcessStatus struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	ReadyState    string `json:"is_ready"`
	HasReadyProbe bool   `json:"has_ready_probe"`
	IsRunning     bool   `json:"is_running"`
	PID           int    `json:"pid"`
	ExitCode      int    `json:"exit_code"`
}

func (status ProcessStatus) IsReady() bool {
	return status.IsRunning && (status.ReadyState == "Ready" || !status.HasReadyProbe)
}

func (status ProcessStatus) IsRunningOrStarting() bool {
	return status.IsRunning || status.Status == "Pending" || status.Status == "Launching" || status.Status == "Restarting"
}

type ProcessEndedError struct {
	ProcessName string
	Status      string
	ExitCode    int
}

func (endedError *ProcessEndedError) Error() string {
	return fmt.Sprintf("%s is %s with exit code %d", endedError.ProcessName, endedError.Status, endedError.ExitCode)
}

type ProcessNotReadyInTimeError struct {
	ProcessName string
	Timeout     time.Duration
	Status      string
}

func (notReadyInTimeError *ProcessNotReadyInTimeError) Error() string {
	return fmt.Sprintf("%s not ready after %s (status %q)", notReadyInTimeError.ProcessName, notReadyInTimeError.Timeout, notReadyInTimeError.Status)
}

// Env is the whole environment of the daemon, which every process inherits: process-compose offers no way to stop that.
type Client struct {
	Binary   string
	StateDir string
	Env      func() (map[string]string, error)
}

const statusPollInterval = 500 * time.Millisecond

type projectFile struct {
	Version   string                           `yaml:"version"`
	Processes map[string]processComposeProcess `yaml:"processes"`
}

type processComposeProcess struct {
	Command        string          `yaml:"command"`
	WorkingDir     string          `yaml:"working_dir,omitempty"`
	Environment    []string        `yaml:"environment,omitempty"`
	LogLocation    string          `yaml:"log_location,omitempty"`
	LogConfig      *logConfig      `yaml:"log_configuration,omitempty"`
	ReadinessProbe *readinessProbe `yaml:"readiness_probe,omitempty"`
	Shutdown       shutdown        `yaml:"shutdown"`
}

type shutdown struct {
	Command        string `yaml:"command,omitempty"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

type logConfig struct {
	Rotation logRotation `yaml:"rotation"`
}

type logRotation struct {
	MaxSizeMB  int `yaml:"max_size_mb"`
	MaxBackups int `yaml:"max_backups"`
}

// Without rotation a log grows without limit.
var everyLogRotation = &logConfig{Rotation: logRotation{MaxSizeMB: 10, MaxBackups: 2}}

type readinessProbe struct {
	Exec             *execProbe    `yaml:"exec,omitempty"`
	HTTPGet          *httpGetProbe `yaml:"http_get,omitempty"`
	PeriodSeconds    int           `yaml:"period_seconds"`
	FailureThreshold int           `yaml:"failure_threshold"`
}

// process-compose kills a process whose probe fails this many times in a row, slow starters included.
const readinessFailureThreshold = 600

type execProbe struct {
	Command string `yaml:"command"`
}

type httpGetProbe struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	Path string `yaml:"path"`
}

// Past it, process-compose sends SIGKILL: without it, a process that ignores SIGTERM blocks every later dev command.
const shutdownTimeoutSeconds = 20

func RenderProject(specs []ProcessSpec) ([]byte, error) {
	project := projectFile{Version: "0.5", Processes: map[string]processComposeProcess{}}
	for _, spec := range specs {
		process := processComposeProcess{
			Command:        spec.Command,
			WorkingDir:     spec.WorkingDir,
			Environment:    nil,
			LogLocation:    spec.LogFile,
			LogConfig:      nil,
			ReadinessProbe: readinessProbeFor(spec.Ready),
			Shutdown:       shutdown{Command: spec.ShutdownCommand, TimeoutSeconds: shutdownTimeoutSeconds},
		}
		if spec.LogFile != "" {
			process.LogConfig = everyLogRotation
		}
		process.Environment = Environ(spec.Env)
		project.Processes[spec.Name] = process
	}
	return yaml.Marshal(project)
}

func Environ(valueByKey map[string]string) []string {
	environ := make([]string, 0, len(valueByKey))
	for _, key := range slices.Sorted(maps.Keys(valueByKey)) {
		environ = append(environ, key+"="+valueByKey[key])
	}
	return environ
}

func readinessProbeFor(ready ReadyProbe) *readinessProbe {
	host := cmp.Or(ready.Host, "127.0.0.1")
	switch {
	case ready.Command != "":
		return &readinessProbe{Exec: &execProbe{Command: ready.Command}, HTTPGet: nil, PeriodSeconds: 1, FailureThreshold: readinessFailureThreshold}
	case ready.Port == 0:
		return nil
	case ready.Path != "":
		return &readinessProbe{Exec: nil, HTTPGet: &httpGetProbe{Host: host, Port: ready.Port, Path: ready.Path}, PeriodSeconds: 1, FailureThreshold: readinessFailureThreshold}
	default:
		return &readinessProbe{Exec: &execProbe{Command: fmt.Sprintf("bash -c 'echo > /dev/tcp/%s/%d'", host, ready.Port)}, HTTPGet: nil, PeriodSeconds: 1, FailureThreshold: readinessFailureThreshold}
	}
}

const (
	socketFileName      = "daemon.sock"
	daemonLogFileName   = "daemon.log"
	environmentFileName = "environment.json"
)

func (client Client) socketPath() string {
	return filepath.Join(client.StateDir, socketFileName)
}

func (client Client) daemonLogPath() string {
	return filepath.Join(client.StateDir, daemonLogFileName)
}

func (client Client) environmentPath() string {
	return filepath.Join(client.StateDir, environmentFileName)
}

// The environment the running daemon started with, and gives every process. Nothing is recorded when the daemon is down.
func (client Client) RecordedEnvironment() (environment map[string]string, isRecorded bool, err error) {
	if !client.IsUp(context.Background()) {
		return nil, false, nil
	}
	content, err := os.ReadFile(client.environmentPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(content, &environment); err != nil {
		return nil, false, fmt.Errorf("read %s: %w", client.environmentPath(), err)
	}
	return environment, true, nil
}

func (client Client) recordEnvironment(environment map[string]string) error {
	content, err := json.Marshal(environment)
	if err != nil {
		return err
	}
	if err := os.WriteFile(client.environmentPath(), content, 0o600); err != nil {
		return err
	}
	return os.Chmod(client.environmentPath(), 0o600)
}

func (client Client) forgetEnvironment() error {
	if err := os.Remove(client.environmentPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Relative to the state folder, because a unix socket path is limited to 107 bytes.
func (client Client) command(ctx context.Context, arguments ...string) *exec.Cmd {
	globalArguments := []string{"--use-uds", "--unix-socket", socketFileName}
	command := exec.CommandContext(ctx, client.Binary, append(arguments, globalArguments...)...)
	command.Dir = client.StateDir
	return command
}

func (client Client) run(ctx context.Context, arguments ...string) ([]byte, error) {
	command := client.command(ctx, arguments...)
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("process-compose %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func (client Client) listProcesses(ctx context.Context) ([]ProcessStatus, error) {
	output, err := client.run(ctx, "process", "list", "--output", "json")
	if err != nil {
		return nil, err
	}
	var processes []ProcessStatus
	if err := json.Unmarshal(output, &processes); err != nil {
		return nil, fmt.Errorf("read process-compose process list: %w", err)
	}
	return processes, nil
}

func (client Client) IsUp(ctx context.Context) bool {
	if _, err := os.Stat(client.socketPath()); err != nil {
		return false
	}
	_, err := client.listProcesses(ctx)
	return err == nil
}

// The daemon starts on an empty project: a project given at start makes the first update restart every process in it.
func (client Client) ensureDaemon(ctx context.Context) error {
	if client.IsUp(ctx) {
		return nil
	}
	if err := os.MkdirAll(client.StateDir, 0o700); err != nil {
		return err
	}
	if err := os.Remove(client.socketPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	emptyProjectPath := filepath.Join(client.StateDir, "empty-project.yaml")
	if err := os.WriteFile(emptyProjectPath, []byte("version: \"0.5\"\nprocesses: {}\n"), 0o644); err != nil {
		return err
	}
	environment, err := client.Env()
	if err != nil {
		return err
	}
	if err := client.recordEnvironment(environment); err != nil {
		return err
	}
	startCommand := client.command(context.WithoutCancel(ctx), "up", "--detached", "--keep-project", "--log-file", daemonLogFileName, "--config", emptyProjectPath)
	startCommand.Env = Environ(environment)
	if output, err := startCommand.CombinedOutput(); err != nil {
		return fmt.Errorf("start process-compose: %w: %s", err, output)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if client.IsUp(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(statusPollInterval):
		}
	}
	return fmt.Errorf("process-compose did not answer on %s, see %s", client.socketPath(), client.daemonLogPath())
}

func (client Client) Apply(ctx context.Context, specs []ProcessSpec) error {
	if len(specs) == 0 && !client.IsUp(ctx) {
		return nil
	}
	if err := client.ensureDaemon(ctx); err != nil {
		return err
	}
	content, err := RenderProject(specs)
	if err != nil {
		return err
	}
	projectPath := filepath.Join(client.StateDir, "project.yaml")
	if err := os.WriteFile(projectPath, content, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(projectPath, 0o600); err != nil {
		return err
	}
	_, err = client.run(ctx, "project", "update", "--config", projectPath)
	return err
}

func (client Client) Statuses(ctx context.Context) (map[string]ProcessStatus, error) {
	statusByName := map[string]ProcessStatus{}
	if !client.IsUp(ctx) {
		return statusByName, nil
	}
	processes, err := client.listProcesses(ctx)
	if err != nil {
		return nil, err
	}
	for _, process := range processes {
		statusByName[process.Name] = process
	}
	return statusByName, nil
}

func (client Client) RestartCommand(processName string) string {
	return fmt.Sprintf("cd %s && %s process restart %s --use-uds --unix-socket %s",
		ShellQuote(client.StateDir), ShellQuote(client.Binary), ShellQuote(processName), socketFileName)
}

func ShellQuote(word string) string {
	return "'" + strings.ReplaceAll(word, "'", `'"'"'`) + "'"
}

func (client Client) Restart(ctx context.Context, processName string) error {
	_, err := client.run(ctx, "process", "restart", processName)
	return err
}

func (client Client) WaitReady(ctx context.Context, processName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		statuses, err := client.Statuses(ctx)
		if err != nil {
			return err
		}
		status, isKnown := statuses[processName]
		switch {
		case isKnown && status.IsReady():
			return nil
		case isKnown && !status.IsRunningOrStarting():
			return &ProcessEndedError{ProcessName: processName, Status: status.Status, ExitCode: status.ExitCode}
		case time.Now().After(deadline):
			return &ProcessNotReadyInTimeError{ProcessName: processName, Timeout: timeout, Status: status.Status}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(statusPollInterval):
		}
	}
}

func (client Client) Shutdown() error {
	if !client.IsUp(context.Background()) {
		return client.forgetEnvironment()
	}
	if _, err := client.run(context.Background(), "down"); err != nil {
		return err
	}
	return client.forgetEnvironment()
}
