package ilotctl

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/personal"
)

func TestEnvironmentListsTheShellBelowEveryOtherSource(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://proxy", "LOG_LEVEL": "info", "REGION": "DEV1"})
	environment.controller.Config.RepoEnv = map[string]map[string]string{"platform": {"REGION": "EU1"}}
	environment.controller.Personal = personal.Settings{Env: map[string]string{"LOG_LEVEL": "debug"}, Apps: nil, Repos: nil}
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	entryByKey := environmentOf(t, environment, "standalone")

	want := map[string]EnvEntry{
		"GOPROXY":   {Key: "GOPROXY", Value: "https://proxy", Source: "shell"},
		"LOG_LEVEL": {Key: "LOG_LEVEL", Value: "debug", Source: "personal"},
		"REGION":    {Key: "REGION", Value: "EU1", Source: "repo"},
	}
	for key, wantEntry := range want {
		if entryByKey[key] != wantEntry {
			t.Errorf("%s = %+v, want %+v", key, entryByKey[key], wantEntry)
		}
	}
	if processEnv := environment.supervisor.runningSpecs["standalone.proj-1234"].Env; processEnv["GOPROXY"] != "" {
		t.Errorf("the process spec repeats the shell, which the daemon already gives it: %v", processEnv)
	}
}

func TestEnvironmentShowsTheShellTheDaemonStartedWith(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://login-shell"})
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	laterCommand := environment.controllerOfANewCommand()
	laterCommand.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://envrc-of-another-shell"})

	entries, err := laterCommand.Environment(environment.featureStack, "standalone")
	if err != nil {
		t.Fatalf("Environment: %v", err)
	}

	goproxyIndex := slices.IndexFunc(entries, func(entry EnvEntry) bool { return entry.Key == "GOPROXY" })
	if goproxyIndex < 0 || entries[goproxyIndex].Value != "https://login-shell" {
		t.Errorf("GOPROXY = %+v, want the value the daemon started with", entries)
	}
	if isUnrecorded, err := laterCommand.RunsWithoutARecordedEnvironment(t.Context()); isUnrecorded || err != nil {
		t.Errorf("RunsWithoutARecordedEnvironment = %t, %v; want false for a daemon ilot started", isUnrecorded, err)
	}
}

func TestADaemonStartedByAnOlderDevHasNoRecordedEnvironment(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.envAtStart = nil
	environment.controller.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://this-shell"})
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	isUnrecorded, err := environment.controller.RunsWithoutARecordedEnvironment(t.Context())
	if !isUnrecorded || err != nil {
		t.Errorf("RunsWithoutARecordedEnvironment = %t, %v; want true", isUnrecorded, err)
	}
	if entry := environmentOf(t, environment, "standalone")["GOPROXY"]; entry.Value != "https://this-shell" || entry.Source != "shell" {
		t.Errorf("GOPROXY = %+v, want this shell for lack of a record", entry)
	}
}

func TestBuildsRunWithTheShellOfTheDaemon(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://proxy", "PATH": "/usr/bin"})

	environment.start(t, environment.master, StartOptions{SkipDependencies: true}, "publicapi")

	if want := []string{"GOPROXY=https://proxy", "PATH=/usr/bin"}; len(*environment.buildEnvirons) != 1 || !slices.Equal((*environment.buildEnvirons)[0], want) {
		t.Errorf("build environments = %v, want one build with %v", *environment.buildEnvirons, want)
	}
}

func TestAContainerGetsNoShellVariable(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ShellEnv = fixedShellEnv(map[string]string{"KEYRING_APP_KEY": "secret"})
	environment.start(t, environment.featureStack, StartOptions{}, "search")

	command := environment.supervisor.runningSpecs["search.proj-1234"].Command
	if strings.Contains(command, "KEYRING_APP_KEY") {
		t.Errorf("command %q passes a shell variable to the image", command)
	}
	if !strings.Contains(command, "-e 'REGION'") {
		t.Errorf("command %q lost the app env", command)
	}
}

func TestRestartStartsTheDaemonAgainWithThisShellAndTheAppsThatRan(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://old"})
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	restart := environment.controllerOfANewCommand()
	restart.ShellEnv = fixedShellEnv(map[string]string{"GOPROXY": "https://new"})
	environment.supervisor.events = nil

	if err := restart.Restart(t.Context(), environment.master); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	if events := environment.supervisor.events; len(events) == 0 || events[0] != "shutdown" {
		t.Errorf("events = %v, want a shutdown first", events)
	}
	running := slices.Sorted(maps.Keys(environment.supervisor.runningSpecs))
	want := []string{"gateway.master", "internalapi.master", "restapi-watch.devbox", "restapi.devbox", "standalone.proj-1234", "temporal.devbox"}
	if !slices.Equal(running, want) {
		t.Errorf("running after restart %v, want %v", running, want)
	}
	recorded, isRecorded, err := environment.supervisor.RecordedEnvironment()
	if err != nil || !isRecorded || recorded["GOPROXY"] != "https://new" {
		t.Errorf("the daemon started with %v, recorded %t, %v; want the shell of the restart", recorded, isRecorded, err)
	}
	if !strings.Contains(environment.output.String(), "stopping every app of every stack") {
		t.Errorf("output = %q, want a line about the stop", environment.output.String())
	}
}

func TestRestartOfADownDaemonBootsIt(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	environment.supervisor.simulateDevboxRestart()

	if err := environment.controllerOfANewCommand().Restart(t.Context(), environment.master); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	if _, isRunning := environment.supervisor.runningSpecs["standalone.proj-1234"]; !isRunning {
		t.Errorf("running after restart %v, want standalone.proj-1234 back", slices.Sorted(maps.Keys(environment.supervisor.runningSpecs)))
	}
	if strings.Contains(environment.output.String(), "stopping every app") {
		t.Errorf("output = %q, announces a stop although nothing ran", environment.output.String())
	}
}
