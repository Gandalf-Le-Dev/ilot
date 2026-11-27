package engine

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
)

func TestEnvironmentListsTheShellBelowEveryOtherSource(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://proxy", "LOG_LEVEL": "info", "REGION": "DEV1"})
	environment.executor.Config.RepoEnv = map[string]map[string]string{"shop": {"REGION": "EU1"}}
	environment.executor.Settings = config.WorkspaceSettings{Repos: nil, Env: map[string]string{"LOG_LEVEL": "debug"}, Apps: nil}
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	entryByKey := environmentOf(t, environment, "standalone")

	want := map[string]plan.EnvEntry{
		"GOPROXY":   {Key: "GOPROXY", Value: "https://proxy", Source: "shell"},
		"LOG_LEVEL": {Key: "LOG_LEVEL", Value: "debug", Source: "global"},
		"REGION":    {Key: "REGION", Value: "EU1", Source: "repo"},
	}
	for key, wantEntry := range want {
		if entryByKey[key] != wantEntry {
			t.Errorf("%s = %+v, want %+v", key, entryByKey[key], wantEntry)
		}
	}
	processEnv := environment.supervisor.runningSpecs["standalone.proj-1234.shop"].Env
	if processEnv["GOPROXY"] != "https://proxy" || processEnv["LOG_LEVEL"] != "debug" || processEnv["REGION"] != "EU1" {
		t.Errorf("the process spec has the environment %v, want every source in it: the daemon adds nothing", processEnv)
	}
}

func TestAChangeOfTheShellReachesAnAppAtItsNextStart(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://old"})
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	laterCommand := environment.executorOfANewCommand()
	laterCommand.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://new"})

	if err := laterCommand.Start(t.Context(), environment.featureStack, []string{"standalone"}, StartOptions{}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if got := environment.supervisor.runningSpecs["standalone.proj-1234.shop"].Env["GOPROXY"]; got != "https://new" {
		t.Errorf("GOPROXY of the process = %q, want the value of the shell files now, with no restart of the daemon", got)
	}
}

func TestBuildsRunWithTheShellEnvironment(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://proxy", "PATH": "/usr/bin"})

	environment.start(t, environment.base, StartOptions{SkipDependencies: true}, "publicapi")

	if want := []string{"GOPROXY=https://proxy", "PATH=/usr/bin"}; len(*environment.buildEnvirons) != 1 || !slices.Equal((*environment.buildEnvirons)[0], want) {
		t.Errorf("build environments = %v, want one build with %v", *environment.buildEnvirons, want)
	}
}

func TestAContainerGetsNoShellVariable(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"API_TOKEN": "secret"})
	environment.start(t, environment.featureStack, StartOptions{}, "search")

	command := environment.supervisor.runningSpecs["search.proj-1234.shop"].Command
	if strings.Contains(command, "API_TOKEN") {
		t.Errorf("command %q passes a shell variable to the image", command)
	}
	if got := environment.supervisor.runningSpecs["search.proj-1234.shop"].Env["API_TOKEN"]; got != "secret" {
		t.Errorf("API_TOKEN of the docker command = %q, want the shell value: docker itself needs PATH and HOME", got)
	}
	if !strings.Contains(command, "-e 'REGION'") {
		t.Errorf("command %q lost the app env", command)
	}
}

func TestADaemonRestartStopsTheDaemonAndStartsTheMembers(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://old"})
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	restart := environment.executorOfANewCommand()
	restart.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://new"})
	environment.supervisor.events = nil

	if err := restart.RestartDaemon(t.Context()); err != nil {
		t.Fatalf("RestartDaemon: %v", err)
	}

	if events := environment.supervisor.events; len(events) == 0 || events[0] != "shutdown" {
		t.Errorf("events = %v, want a shutdown first", events)
	}
	running := slices.Sorted(maps.Keys(environment.supervisor.runningSpecs))
	if want := []string{"standalone.proj-1234.shop"}; !slices.Equal(running, want) {
		t.Errorf("running after restart %v, want %v: the members of the stacks", running, want)
	}
	if got := environment.supervisor.runningSpecs["standalone.proj-1234.shop"].Env["GOPROXY"]; got != "https://new" {
		t.Errorf("GOPROXY after the restart = %q, want the shell of the restart", got)
	}
	if !strings.Contains(environment.output.String(), "stopping every app of every stack, and the ilot daemon") {
		t.Errorf("output = %q, want a line about the stop", environment.output.String())
	}
}

func TestADaemonRestartOfADownDaemonStartsTheMembers(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	environment.supervisor.simulateDaemonRestart()

	if err := environment.executorOfANewCommand().RestartDaemon(t.Context()); err != nil {
		t.Fatalf("RestartDaemon: %v", err)
	}

	if _, isRunning := environment.supervisor.runningSpecs["standalone.proj-1234.shop"]; !isRunning {
		t.Errorf("running after restart %v, want standalone.proj-1234.shop back", slices.Sorted(maps.Keys(environment.supervisor.runningSpecs)))
	}
	if strings.Contains(environment.output.String(), "stopping every app") {
		t.Errorf("output = %q, announces a stop although nothing ran", environment.output.String())
	}
}

// A slow profile must not hold the state lock: every other ilot would wait for it, and give up after the lock timeout.
func TestACommandReadsTheLoginShellBeforeItTakesTheStateLock(t *testing.T) {
	commands := map[string]func(environment testEnvironment) error{
		"start": func(environment testEnvironment) error {
			return environment.executor.Start(t.Context(), environment.base, []string{"standalone"}, StartOptions{})
		},
		"restart": func(environment testEnvironment) error {
			return environment.executor.RestartApps(t.Context(), environment.base, []string{"standalone"})
		},
		"start --all": func(environment testEnvironment) error { return environment.executor.StartAll(t.Context()) },
		"start of a stack": func(environment testEnvironment) error {
			environment.supervisor.simulateDaemonRestart()
			return environment.executor.StartStack(t.Context(), environment.base)
		},
	}
	for name, command := range commands {
		t.Run(name, func(t *testing.T) {
			environment := newTestEnvironment(t)
			environment.start(t, environment.base, StartOptions{}, "standalone")
			otherCommand := environment.executor.Store
			otherCommand.LockTimeout = 50 * time.Millisecond
			isLockHeldDuringCapture := false
			environment.executor.ShellEnv = func() (map[string]string, map[string]string, error) {
				release, err := otherCommand.Lock(t.Context())
				if err != nil {
					isLockHeldDuringCapture = true
					return map[string]string{}, map[string]string{}, nil
				}
				return map[string]string{}, map[string]string{}, release()
			}

			if err := command(environment); err != nil {
				t.Fatal(err)
			}

			if isLockHeldDuringCapture {
				t.Errorf("ilot %s held the state lock while the login shell ran", name)
			}
		})
	}
}
