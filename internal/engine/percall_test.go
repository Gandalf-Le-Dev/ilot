package engine

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
)

// A command from another terminal, or after a profile edit, gets another shell environment.
func TestAStartInAStackSendsNoCallForAnotherStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"PATH": "/usr/bin", "TMPDIR": "/tmp/first"})
	environment.start(t, "retry-jobs", StartOptions{}, "publicapi")
	hashBefore := environment.supervisor.runningSpecs["publicapi.retry-jobs.shop"].Hash()
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"PATH": "/usr/bin", "TMPDIR": "/tmp/second"})
	environment.supervisor.calls = nil
	environment.supervisor.startedInOrder = nil

	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	if hashAfter := environment.supervisor.runningSpecs["publicapi.retry-jobs.shop"].Hash(); hashAfter != hashBefore {
		t.Error("a start in task-1234 from another terminal replaced publicapi.retry-jobs.shop")
	}
	for _, call := range environment.supervisor.calls {
		if strings.Contains(call, "retry-jobs") || strings.Contains(call, ".base.") {
			t.Errorf("calls = %v, want none for another stack: %q", environment.supervisor.calls, call)
		}
	}
	if want := []string{"standalone.task-1234.shop"}; !slices.Equal(environment.supervisor.startedInOrder, want) {
		t.Errorf("started %v, want %v only", environment.supervisor.startedInOrder, want)
	}
}

func TestAStartLeavesARunningDependencyWhoseSpecificationChanged(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"PATH": "/usr/bin"})
	environment.start(t, environment.base, StartOptions{}, "publicapi")
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"PATH": "/usr/local/bin:/usr/bin"})
	environment.supervisor.calls = nil

	environment.start(t, environment.base, StartOptions{}, "publicapi")

	for _, call := range environment.supervisor.calls {
		if !strings.Contains(call, "publicapi.base.shop") {
			t.Errorf("calls = %v, want calls for the named publicapi only", environment.supervisor.calls)
		}
	}
}

func TestAStartOfAChangedAppReplacesItOneTimeAndSaysSo(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"PATH": "/usr/bin"})
	environment.start(t, environment.base, StartOptions{}, "standalone")
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"PATH": "/usr/local/bin:/usr/bin"})
	environment.supervisor.startedInOrder = nil
	environment.supervisor.restarted = nil
	environment.output.Reset()

	environment.start(t, environment.base, StartOptions{}, "standalone")

	if want := []string{"standalone.base.shop"}; !slices.Equal(environment.supervisor.startedInOrder, want) || len(environment.supervisor.restarted) != 0 {
		t.Errorf("started %v and restarted %v, want one start: a Put that replaces it, and no restart on top", environment.supervisor.startedInOrder, environment.supervisor.restarted)
	}
	if strings.Count(environment.output.String(), "restarting standalone.base.shop") != 1 {
		t.Errorf("output %q, want one line for the replaced process", environment.output.String())
	}
}

func TestAStartReplacesTheAppsOfItsStackWhoseTemplatesItChanged(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Config.Apps["reader"] = config.App{Run: `reader --api={{port "internalapi"}}`, DependsOn: []string{"internalapi"}}
	environment.start(t, environment.featureStack, StartOptions{}, "reader")
	environment.supervisor.startedInOrder = nil
	environment.supervisor.waitedReady = nil
	environment.output.Reset()

	environment.start(t, environment.featureStack, StartOptions{}, "internalapi")

	readerCommand := environment.supervisor.runningSpecs["reader.task-1234.shop"].Command
	if want := "reader --api=" + strconv.Itoa(environment.appPort(t, "task-1234", "internalapi")); readerCommand != want {
		t.Errorf("reader command = %q, want %q: the stack now runs its own internalapi", readerCommand, want)
	}
	if !strings.Contains(environment.output.String(), "restarting reader.task-1234.shop: this start changed its specification") {
		t.Errorf("output %q, want the line that says why reader restarts", environment.output.String())
	}
	if !slices.Contains(environment.supervisor.waitedReady, "reader.task-1234.shop") {
		t.Errorf("waited for %v, want reader too: a replaced app is ready before the start ends", environment.supervisor.waitedReady)
	}
}

func TestAStartSaysNothingOfADependentThatAlreadyRunsItsNewSpecification(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Config.Apps["reader"] = config.App{Run: `reader --api={{port "internalapi"}}`, DependsOn: []string{"internalapi"}}
	environment.start(t, environment.featureStack, StartOptions{}, "reader")
	environment.start(t, environment.featureStack, StartOptions{}, "internalapi")
	if err := environment.executor.Stop(t.Context(), environment.featureStack, []string{"internalapi"}); err != nil {
		t.Fatal(err)
	}
	environment.output.Reset()
	environment.supervisor.calls = nil

	environment.start(t, environment.featureStack, StartOptions{}, "internalapi")

	if strings.Contains(environment.output.String(), "restarting reader") || slices.Contains(environment.supervisor.calls, "put reader.task-1234.shop") {
		t.Errorf("output %q, calls %v; want no restart of reader, which already runs with the port of this internalapi", environment.output.String(), environment.supervisor.calls)
	}
}

func TestStopRemovesTheContainerOfAnAppThatTheDaemonDoesNotHold(t *testing.T) {
	environment := newTestEnvironment(t)
	var removed []string
	environment.executor.RemoveContainer = func(_ context.Context, name string) error {
		removed = append(removed, name)
		return nil
	}
	environment.start(t, environment.featureStack, StartOptions{SkipDependencies: true}, "search-cache")
	environment.supervisor.simulateDaemonRestart()

	if err := environment.executor.Stop(t.Context(), environment.featureStack, []string{"search-cache"}); err != nil {
		t.Fatal(err)
	}

	if want := []string{"ilot_search-cache_task-1234_shop"}; !slices.Equal(removed, want) {
		t.Errorf("removed containers %v, want %v: no daemon stops it", removed, want)
	}
}

func TestStopLeavesTheRemovalOfAHeldContainerToItsStopCommand(t *testing.T) {
	environment := newTestEnvironment(t)
	var removed []string
	environment.executor.RemoveContainer = func(_ context.Context, name string) error {
		removed = append(removed, name)
		return nil
	}
	environment.start(t, environment.featureStack, StartOptions{SkipDependencies: true}, "search-cache")

	if err := environment.executor.Stop(t.Context(), environment.featureStack, []string{"search-cache"}); err != nil {
		t.Fatal(err)
	}

	if len(removed) != 0 || environment.isApplied("search-cache.task-1234.shop") {
		t.Errorf("removed %v, applied %t; want the daemon to stop the container with docker stop", removed, environment.isApplied("search-cache.task-1234.shop"))
	}
}

func TestTheSessionOfTheTerminalReachesAnAppOutsideItsHash(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ShellEnv = func() (map[string]string, map[string]string, error) {
		return map[string]string{"PATH": "/usr/bin"}, map[string]string{"SSH_AUTH_SOCK": "/tmp/agent"}, nil
	}

	environment.start(t, environment.base, StartOptions{}, "standalone")

	spec := environment.supervisor.runningSpecs["standalone.base.shop"]
	if spec.Session["SSH_AUTH_SOCK"] != "/tmp/agent" || spec.Env["SSH_AUTH_SOCK"] != "" {
		t.Errorf("env = %v, session = %v; want the agent in the session part only", spec.Env, spec.Session)
	}
	if len(*environment.buildEnvirons) > 0 && !slices.Contains((*environment.buildEnvirons)[0], "SSH_AUTH_SOCK=/tmp/agent") {
		t.Errorf("build environ = %v, want the agent of the terminal", (*environment.buildEnvirons)[0])
	}
}

// After an SSH reconnect the old SSH_AUTH_SOCK is dead, and only the session differs, so the hash is the same.
func TestARestartGivesTheAppTheSessionOfTheNewTerminal(t *testing.T) {
	environment := newTestEnvironment(t)
	sessionOf := func(agent string) func() (map[string]string, map[string]string, error) {
		return func() (map[string]string, map[string]string, error) {
			return map[string]string{"PATH": "/usr/bin"}, map[string]string{"SSH_AUTH_SOCK": agent}, nil
		}
	}
	environment.executor.ShellEnv = sessionOf("/tmp/first-agent")
	environment.start(t, environment.base, StartOptions{}, "standalone")

	environment.executor.ShellEnv = sessionOf("/tmp/second-agent")
	environment.start(t, environment.base, StartOptions{}, "standalone")

	if got := environment.supervisor.runningSpecs["standalone.base.shop"].Session["SSH_AUTH_SOCK"]; got != "/tmp/first-agent" {
		t.Errorf("SSH_AUTH_SOCK after a second ilot start = %q, want the first agent: a start leaves a running app as it is", got)
	}

	if err := environment.executor.RestartApps(t.Context(), environment.base, []string{"standalone"}); err != nil {
		t.Fatal(err)
	}

	if got := environment.supervisor.runningSpecs["standalone.base.shop"].Session["SSH_AUTH_SOCK"]; got != "/tmp/second-agent" {
		t.Errorf("SSH_AUTH_SOCK after ilot restart = %q, want the agent of the new terminal", got)
	}
}
