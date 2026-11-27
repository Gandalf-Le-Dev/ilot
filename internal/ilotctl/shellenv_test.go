package ilotctl

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
)

func TestEnvironmentListsTheShellBelowEveryOtherSource(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://proxy", "LOG_LEVEL": "info", "REGION": "DEV1"})
	environment.controller.Config.RepoEnv = map[string]map[string]string{"platform": {"REGION": "EU1"}}
	environment.controller.Settings = config.WorkspaceSettings{Repos: nil, Env: map[string]string{"LOG_LEVEL": "debug"}, Apps: nil}
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	entryByKey := environmentOf(t, environment, "standalone")

	want := map[string]EnvEntry{
		"GOPROXY":   {Key: "GOPROXY", Value: "https://proxy", Source: "shell"},
		"LOG_LEVEL": {Key: "LOG_LEVEL", Value: "debug", Source: "global"},
		"REGION":    {Key: "REGION", Value: "EU1", Source: "repo"},
	}
	for key, wantEntry := range want {
		if entryByKey[key] != wantEntry {
			t.Errorf("%s = %+v, want %+v", key, entryByKey[key], wantEntry)
		}
	}
	processEnv := environment.supervisor.runningSpecs["standalone.proj-1234.platform"].Env
	if processEnv["GOPROXY"] != "https://proxy" || processEnv["LOG_LEVEL"] != "debug" || processEnv["REGION"] != "EU1" {
		t.Errorf("the process spec has the environment %v, want every source in it: the daemon adds nothing", processEnv)
	}
}

func TestAChangeOfTheShellReachesAnAppAtItsNextStart(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://old"})
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	laterCommand := environment.controllerOfANewCommand()
	laterCommand.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://new"})

	if err := laterCommand.Start(t.Context(), environment.featureStack, []string{"standalone"}, StartOptions{}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if got := environment.supervisor.runningSpecs["standalone.proj-1234.platform"].Env["GOPROXY"]; got != "https://new" {
		t.Errorf("GOPROXY of the process = %q, want the value of the shell files now, with no restart of the daemon", got)
	}
}

func TestBuildsRunWithTheShellEnvironment(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://proxy", "PATH": "/usr/bin"})

	environment.start(t, environment.base, StartOptions{SkipDependencies: true}, "publicapi")

	if want := []string{"GOPROXY=https://proxy", "PATH=/usr/bin"}; len(*environment.buildEnvirons) != 1 || !slices.Equal((*environment.buildEnvirons)[0], want) {
		t.Errorf("build environments = %v, want one build with %v", *environment.buildEnvirons, want)
	}
}

func TestAContainerGetsNoShellVariable(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ShellEnv = fixedShellEnv(map[string]string{"API_TOKEN": "secret"})
	environment.start(t, environment.featureStack, StartOptions{}, "search")

	command := environment.supervisor.runningSpecs["search.proj-1234.platform"].Command
	if strings.Contains(command, "API_TOKEN") {
		t.Errorf("command %q passes a shell variable to the image", command)
	}
	if got := environment.supervisor.runningSpecs["search.proj-1234.platform"].Env["API_TOKEN"]; got != "secret" {
		t.Errorf("API_TOKEN of the docker command = %q, want the shell value: docker itself needs PATH and HOME", got)
	}
	if !strings.Contains(command, "-e 'REGION'") {
		t.Errorf("command %q lost the app env", command)
	}
}

func TestRestartStopsTheDaemonAndStartsTheAppsThatRan(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://old"})
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	restart := environment.controllerOfANewCommand()
	restart.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://new"})
	environment.supervisor.events = nil

	if err := restart.Restart(t.Context(), environment.base); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	if events := environment.supervisor.events; len(events) == 0 || events[0] != "shutdown" {
		t.Errorf("events = %v, want a shutdown first", events)
	}
	running := slices.Sorted(maps.Keys(environment.supervisor.runningSpecs))
	want := []string{"gateway.base.platform", "internalapi.base.platform", "restapi-watch.base.platform", "restapi.base.platform", "standalone.proj-1234.platform", "temporal.base.platform"}
	if !slices.Equal(running, want) {
		t.Errorf("running after restart %v, want %v", running, want)
	}
	if got := environment.supervisor.runningSpecs["standalone.proj-1234.platform"].Env["GOPROXY"]; got != "https://new" {
		t.Errorf("GOPROXY after the restart = %q, want the shell of the restart", got)
	}
	if !strings.Contains(environment.output.String(), "stopping every app of every stack") {
		t.Errorf("output = %q, want a line about the stop", environment.output.String())
	}
}

func TestRestartOfADownDaemonBootsIt(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	environment.supervisor.simulateDaemonRestart()

	if err := environment.controllerOfANewCommand().Restart(t.Context(), environment.base); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	if _, isRunning := environment.supervisor.runningSpecs["standalone.proj-1234.platform"]; !isRunning {
		t.Errorf("running after restart %v, want standalone.proj-1234.platform back", slices.Sorted(maps.Keys(environment.supervisor.runningSpecs)))
	}
	if strings.Contains(environment.output.String(), "stopping every app") {
		t.Errorf("output = %q, announces a stop although nothing ran", environment.output.String())
	}
}
