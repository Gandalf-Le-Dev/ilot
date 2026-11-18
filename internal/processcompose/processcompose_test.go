package processcompose

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type renderedProject struct {
	Processes map[string]struct {
		Command     string   `yaml:"command"`
		WorkingDir  string   `yaml:"working_dir"`
		Environment []string `yaml:"environment"`
		LogLocation string   `yaml:"log_location"`
		LogConfig   struct {
			Rotation struct {
				MaxSizeMB  int `yaml:"max_size_mb"`
				MaxBackups int `yaml:"max_backups"`
			} `yaml:"rotation"`
		} `yaml:"log_configuration"`
		ReadinessProbe struct {
			FailureThreshold int `yaml:"failure_threshold"`
			Exec             struct {
				Command string `yaml:"command"`
			} `yaml:"exec"`
			HTTPGet struct {
				Host string `yaml:"host"`
				Port int    `yaml:"port"`
				Path string `yaml:"path"`
			} `yaml:"http_get"`
		} `yaml:"readiness_probe"`
	} `yaml:"processes"`
}

func TestRenderProjectWritesCommandEnvironmentAndProbes(t *testing.T) {
	content, err := RenderProject([]ProcessSpec{
		{
			Name: "internalapi.master", Command: "platform api internalapi", WorkingDir: "/src",
			Env: map[string]string{"B": "2", "A": "1"}, LogFile: "/logs/internalapi.log",
			Ready: ReadyProbe{Port: 20001, Path: "/health"},
		},
		{Name: "gateway.master", Command: "gatewayd", Ready: ReadyProbe{Port: 20000, Path: ""}},
		{Name: "oneshot.master", Command: "true"},
	})
	if err != nil {
		t.Fatalf("RenderProject: %v", err)
	}
	var parsed renderedProject
	if err := yaml.Unmarshal(content, &parsed); err != nil {
		t.Fatalf("output is not YAML: %v\n%s", err, content)
	}

	api := parsed.Processes["internalapi.master"]
	if api.Command != "platform api internalapi" || api.WorkingDir != "/src" || api.LogLocation != "/logs/internalapi.log" {
		t.Errorf("internalapi = %+v", api)
	}
	if rotation := api.LogConfig.Rotation; rotation.MaxSizeMB != 10 || rotation.MaxBackups != 2 {
		t.Errorf("log rotation = %+v, want 10 MB and 2 backups", rotation)
	}
	if len(api.Environment) != 2 || api.Environment[0] != "A=1" || api.Environment[1] != "B=2" {
		t.Errorf("environment = %v, want sorted KEY=value pairs", api.Environment)
	}
	if probe := api.ReadinessProbe.HTTPGet; probe.Port != 20001 || probe.Path != "/health" || probe.Host != "127.0.0.1" {
		t.Errorf("http probe = %+v", probe)
	}
	if threshold := api.ReadinessProbe.FailureThreshold; threshold < 600 {
		t.Errorf("failure_threshold = %d: process-compose kills an app that is not ready after that many checks", threshold)
	}
	if command := parsed.Processes["gateway.master"].ReadinessProbe.Exec.Command; command != "bash -c 'echo > /dev/tcp/127.0.0.1/20000'" {
		t.Errorf("tcp probe = %q", command)
	}
	if oneshot := parsed.Processes["oneshot.master"]; oneshot.ReadinessProbe.Exec.Command != "" || oneshot.ReadinessProbe.HTTPGet.Port != 0 {
		t.Errorf("a process without a ready port got a probe: %+v", oneshot.ReadinessProbe)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	listenerAddress, isTCP := listener.Addr().(*net.TCPAddr)
	if !isTCP {
		t.Fatalf("listener address %v is not TCP", listener.Addr())
	}
	return listenerAddress.Port
}

func TestClientAgainstARealDaemon(t *testing.T) {
	binary, err := exec.LookPath("process-compose")
	if err != nil {
		t.Skip("process-compose is not on PATH")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on PATH")
	}
	stateDirPastTheUnixSocketPathLimit := filepath.Join(t.TempDir(), strings.Repeat("deep-folder/", 10))
	client := Client{Binary: binary, StateDir: stateDirPastTheUnixSocketPathLimit, Env: daemonEnvironmentForTests}
	t.Cleanup(func() {
		if err := client.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	ctx := t.Context()

	if statuses, err := client.Statuses(ctx); err != nil || len(statuses) != 0 {
		t.Fatalf("Statuses before the daemon runs = %v, %v; want empty and no error", statuses, err)
	}

	alphaPort := freePort(t)
	alpha := ProcessSpec{
		Name:    "alpha.test",
		Command: fmt.Sprintf("python3 -m http.server %d --bind 127.0.0.1", alphaPort),
		LogFile: filepath.Join(client.StateDir, "alpha.log"),
		Ready:   ReadyProbe{Port: alphaPort},
	}
	if err := client.Apply(ctx, []ProcessSpec{alpha}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := client.WaitReady(ctx, alpha.Name, 20*time.Second); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	statuses, err := client.Statuses(ctx)
	if err != nil {
		t.Fatalf("Statuses: %v", err)
	}
	firstPID := statuses[alpha.Name].PID
	if !statuses[alpha.Name].IsRunning || firstPID == 0 {
		t.Fatalf("alpha status = %+v", statuses[alpha.Name])
	}

	if err := client.Restart(ctx, alpha.Name); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if err := client.WaitReady(ctx, alpha.Name, 20*time.Second); err != nil {
		t.Fatalf("WaitReady after restart: %v", err)
	}
	statusesAfterRestart, err := client.Statuses(ctx)
	if err != nil {
		t.Fatalf("Statuses after restart: %v", err)
	}
	if statusesAfterRestart[alpha.Name].PID == firstPID {
		t.Errorf("Restart kept pid %d", firstPID)
	}

	if !client.IsUp(ctx) {
		t.Fatal("IsUp = false while the daemon runs")
	}
	pidBeforeCommand := statusesAfterRestart[alpha.Name].PID
	if output, err := exec.CommandContext(ctx, "bash", "-c", client.RestartCommand(alpha.Name)).CombinedOutput(); err != nil {
		t.Fatalf("RestartCommand: %v\n%s", err, output)
	}
	if err := client.WaitReady(ctx, alpha.Name, 20*time.Second); err != nil {
		t.Fatalf("WaitReady after RestartCommand: %v", err)
	}
	statusesAfterCommand, err := client.Statuses(ctx)
	if err != nil {
		t.Fatalf("Statuses after RestartCommand: %v", err)
	}
	if statusesAfterCommand[alpha.Name].PID == pidBeforeCommand {
		t.Errorf("RestartCommand kept pid %d", pidBeforeCommand)
	}

	if err := client.Apply(ctx, nil); err != nil {
		t.Fatalf("Apply(nil): %v", err)
	}
	statusesAfterRemoval, err := client.Statuses(ctx)
	if err != nil {
		t.Fatalf("Statuses after removal: %v", err)
	}
	if len(statusesAfterRemoval) != 0 {
		t.Errorf("Statuses after removing every process = %v", statusesAfterRemoval)
	}
}

func TestIsRunningOrStartingCoversTheStartingStatuses(t *testing.T) {
	for _, testCase := range []struct {
		status                  ProcessStatus
		wantIsRunningOrStarting bool
	}{
		{status: ProcessStatus{Name: "alpha", Status: "Running", ReadyState: "Ready", HasReadyProbe: true, IsRunning: true, PID: 1, ExitCode: 0}, wantIsRunningOrStarting: true},
		{status: ProcessStatus{Name: "alpha", Status: "Pending", ReadyState: "", HasReadyProbe: true, IsRunning: false, PID: 0, ExitCode: 0}, wantIsRunningOrStarting: true},
		{status: ProcessStatus{Name: "alpha", Status: "Launching", ReadyState: "", HasReadyProbe: true, IsRunning: false, PID: 0, ExitCode: 0}, wantIsRunningOrStarting: true},
		{status: ProcessStatus{Name: "alpha", Status: "Restarting", ReadyState: "", HasReadyProbe: true, IsRunning: false, PID: 0, ExitCode: 0}, wantIsRunningOrStarting: true},
		{status: ProcessStatus{Name: "alpha", Status: "Completed", ReadyState: "", HasReadyProbe: true, IsRunning: false, PID: 0, ExitCode: 125}, wantIsRunningOrStarting: false},
		{status: ProcessStatus{Name: "alpha", Status: "Error", ReadyState: "", HasReadyProbe: true, IsRunning: false, PID: 0, ExitCode: 1}, wantIsRunningOrStarting: false},
	} {
		if got := testCase.status.IsRunningOrStarting(); got != testCase.wantIsRunningOrStarting {
			t.Errorf("IsRunningOrStarting(%s) = %t, want %t", testCase.status.Status, got, testCase.wantIsRunningOrStarting)
		}
	}
}

func TestWaitReadyTellsAnEndedProcessFromASlowOne(t *testing.T) {
	binary, err := exec.LookPath("process-compose")
	if err != nil {
		t.Skip("process-compose is not on PATH")
	}
	client := Client{Binary: binary, StateDir: t.TempDir(), Env: daemonEnvironmentForTests}
	t.Cleanup(func() {
		if err := client.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	ctx := t.Context()
	ended := ProcessSpec{Name: "ended.test", Command: "exit 3"}
	slow := ProcessSpec{Name: "slow.test", Command: "sleep 60", Ready: ReadyProbe{Port: freePort(t)}}
	if err := client.Apply(ctx, []ProcessSpec{ended, slow}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	endedWaitErr := client.WaitReady(ctx, ended.Name, 20*time.Second)
	var endedError *ProcessEndedError
	if !errors.As(endedWaitErr, &endedError) || endedError.ProcessName != ended.Name || endedError.ExitCode != 3 {
		t.Errorf("WaitReady(%s) = %v, want a ProcessEndedError with exit code 3", ended.Name, endedWaitErr)
	}

	slowWaitErr := client.WaitReady(ctx, slow.Name, time.Second)
	var notReadyInTimeError *ProcessNotReadyInTimeError
	if !errors.As(slowWaitErr, &notReadyInTimeError) || errors.As(slowWaitErr, &endedError) {
		t.Errorf("WaitReady(%s) = %v, want a ProcessNotReadyInTimeError", slow.Name, slowWaitErr)
	}
}

func TestApplyStopsWaitingForTheDaemonWhenTheContextEnds(t *testing.T) {
	binaryThatStartsNoDaemon, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true is not on PATH")
	}
	client := Client{Binary: binaryThatStartsNoDaemon, StateDir: t.TempDir(), Env: daemonEnvironmentForTests}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	startedAt := time.Now()
	err = client.Apply(ctx, []ProcessSpec{{Name: "alpha.test", Command: "true"}})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Apply = %v, want the context error", err)
	}
	if waited := time.Since(startedAt); waited > 2*time.Second {
		t.Errorf("Apply waited %s after its context ended", waited)
	}
}

func TestRenderProjectProbesAContainer(t *testing.T) {
	content, err := RenderProject([]ProcessSpec{
		{Name: "search.stack", Command: "docker run search", Ready: ReadyProbe{Host: "dev-stack-search", Port: 16271}},
		{Name: "cache.stack", Command: "docker run cache", Ready: ReadyProbe{Command: "docker exec dev-stack-cache sh -c 'pg_isready -U postgres'"}},
	})
	if err != nil {
		t.Fatalf("RenderProject: %v", err)
	}
	var parsed renderedProject
	if err := yaml.Unmarshal(content, &parsed); err != nil {
		t.Fatal(err)
	}
	if command := parsed.Processes["search.stack"].ReadinessProbe.Exec.Command; command != "bash -c 'echo > /dev/tcp/dev-stack-search/16271'" {
		t.Errorf("search probe = %q", command)
	}
	if command := parsed.Processes["cache.stack"].ReadinessProbe.Exec.Command; command != "docker exec dev-stack-cache sh -c 'pg_isready -U postgres'" {
		t.Errorf("cache probe = %q", command)
	}
}

func TestRenderProjectGivesEveryProcessAShutdownTimeout(t *testing.T) {
	content, err := RenderProject([]ProcessSpec{
		{Name: "cache.stack", Command: "docker run cache", ShutdownCommand: "docker stop --time 10 dev-stack-cache"},
		{Name: "api.stack", Command: "api"},
	})
	if err != nil {
		t.Fatalf("RenderProject: %v", err)
	}
	var project struct {
		Processes map[string]struct {
			Shutdown struct {
				Command        string `yaml:"command"`
				TimeoutSeconds int    `yaml:"timeout_seconds"`
			} `yaml:"shutdown"`
		} `yaml:"processes"`
	}
	if err := yaml.Unmarshal(content, &project); err != nil {
		t.Fatal(err)
	}
	if shutdown := project.Processes["cache.stack"].Shutdown; shutdown.Command != "docker stop --time 10 dev-stack-cache" || shutdown.TimeoutSeconds != shutdownTimeoutSeconds {
		t.Errorf("cache shutdown = %+v", shutdown)
	}
	if shutdown := project.Processes["api.stack"].Shutdown; shutdown.Command != "" || shutdown.TimeoutSeconds != shutdownTimeoutSeconds {
		t.Errorf("api shutdown = %+v", shutdown)
	}
}

func TestRemovingAProcessThatIgnoresSIGTERMRunsItsShutdownCommand(t *testing.T) {
	binary, err := exec.LookPath("process-compose")
	if err != nil {
		t.Skip("process-compose is not on PATH")
	}
	client := Client{Binary: binary, StateDir: t.TempDir(), Env: daemonEnvironmentForTests}
	t.Cleanup(func() {
		if err := client.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	if err := os.WriteFile(filepath.Join(client.StateDir, "project.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	stopFile := filepath.Join(t.TempDir(), "stop")
	stubborn := ProcessSpec{
		Name:            "stubborn.test",
		Command:         fmt.Sprintf("trap '' TERM; while [ ! -e %s ]; do sleep 0.2; done", stopFile),
		ShutdownCommand: "touch " + stopFile,
	}
	if err := client.Apply(t.Context(), []ProcessSpec{stubborn}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for attempt := 0; attempt < 50; attempt++ {
		statuses, err := client.Statuses(t.Context())
		if err == nil && statuses[stubborn.Name].IsRunning {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if info, err := os.Stat(filepath.Join(client.StateDir, "project.yaml")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("project file = %v, %v; want mode 600, it holds the env of every app", info, err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	if err := client.Apply(ctx, nil); err != nil {
		t.Fatalf("Apply(nil) = %v, want the process stopped by its shutdown command", err)
	}
}

func daemonEnvironmentForTests() (map[string]string, error) {
	return map[string]string{"PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME")}, nil
}

func TestTheDaemonGivesEveryProcessItsOwnEnvironmentAndRecordsIt(t *testing.T) {
	binary, err := exec.LookPath("process-compose")
	if err != nil {
		t.Skip("process-compose is not on PATH")
	}
	t.Setenv("DEV_TEST_LEAKED_FROM_THE_SHELL", "leaked")
	client := Client{Binary: binary, StateDir: t.TempDir(), Env: func() (map[string]string, error) {
		return map[string]string{"PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME"), "DEV_TEST_GIVEN_TO_THE_DAEMON": "given"}, nil
	}}
	t.Cleanup(func() {
		if err := client.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	ctx := t.Context()
	if _, isRecorded, err := client.RecordedEnvironment(); isRecorded || err != nil {
		t.Fatalf("RecordedEnvironment before the daemon runs = recorded %t, %v; want nothing", isRecorded, err)
	}
	environmentFile := filepath.Join(client.StateDir, "process-environment.txt")
	printer := ProcessSpec{Name: "printer.test", Command: "env > " + ShellQuote(environmentFile) + " && sleep 60", Env: map[string]string{"DEV_TEST_OWN": "own"}}
	if err := client.Apply(ctx, []ProcessSpec{printer}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var processEnvironment string
	for deadline := time.Now().Add(20 * time.Second); processEnvironment == "" && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		content, err := os.ReadFile(environmentFile)
		if err == nil && strings.Contains(string(content), "DEV_TEST_OWN=own") {
			processEnvironment = string(content)
		}
	}

	if processEnvironment == "" {
		t.Fatal("the process never wrote its environment")
	}
	if !strings.Contains(processEnvironment, "DEV_TEST_GIVEN_TO_THE_DAEMON=given\n") {
		t.Errorf("the process lacks the variable given to the daemon:\n%s", processEnvironment)
	}
	if strings.Contains(processEnvironment, "DEV_TEST_LEAKED_FROM_THE_SHELL") {
		t.Errorf("the process got a variable of the shell that started the daemon:\n%s", processEnvironment)
	}
	recorded, isRecorded, err := client.RecordedEnvironment()
	if err != nil || !isRecorded || recorded["DEV_TEST_GIVEN_TO_THE_DAEMON"] != "given" || recorded["PATH"] != os.Getenv("PATH") {
		t.Errorf("RecordedEnvironment = %v, recorded %t, %v; want the environment the daemon started with", recorded, isRecorded, err)
	}
	info, err := os.Stat(client.environmentPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("environment file mode = %v, %v; want 0600, it holds the keyring key", info, err)
	}

	if err := client.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if _, isRecorded, err := client.RecordedEnvironment(); isRecorded || err != nil {
		t.Errorf("RecordedEnvironment after Shutdown = recorded %t, %v; want nothing", isRecorded, err)
	}
	if _, err := os.Stat(client.environmentPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the environment file outlives the daemon: %v", err)
	}
}
