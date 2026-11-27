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
	environment.start(t, "proj-5678", StartOptions{}, "publicapi")
	hashBefore := environment.supervisor.runningSpecs["publicapi.proj-5678.platform"].Hash()
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"PATH": "/usr/bin", "TMPDIR": "/tmp/second"})
	environment.supervisor.calls = nil
	environment.supervisor.startedInOrder = nil

	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	if hashAfter := environment.supervisor.runningSpecs["publicapi.proj-5678.platform"].Hash(); hashAfter != hashBefore {
		t.Error("a start in proj-1234 from another terminal replaced publicapi.proj-5678.platform")
	}
	for _, call := range environment.supervisor.calls {
		if strings.Contains(call, "proj-5678") || strings.Contains(call, ".base.") {
			t.Errorf("calls = %v, want none for another stack: %q", environment.supervisor.calls, call)
		}
	}
	if want := []string{"standalone.proj-1234.platform"}; !slices.Equal(environment.supervisor.startedInOrder, want) {
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
		if !strings.Contains(call, "publicapi.base.platform") {
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

	if want := []string{"standalone.base.platform"}; !slices.Equal(environment.supervisor.startedInOrder, want) || len(environment.supervisor.restarted) != 0 {
		t.Errorf("started %v and restarted %v, want one start: a Put that replaces it, and no restart on top", environment.supervisor.startedInOrder, environment.supervisor.restarted)
	}
	if strings.Count(environment.output.String(), "restarting standalone.base.platform") != 1 {
		t.Errorf("output %q, want one line for the replaced process", environment.output.String())
	}
}

func TestAStartReplacesTheAppsOfItsStackWhoseTemplatesItChanged(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Config.Apps["reader"] = config.App{Run: `reader --api={{port "internalapi"}}`, DependsOn: []string{"internalapi"}}
	environment.start(t, environment.featureStack, StartOptions{}, "reader")
	environment.supervisor.startedInOrder = nil
	environment.output.Reset()

	environment.start(t, environment.featureStack, StartOptions{}, "internalapi")

	readerCommand := environment.supervisor.runningSpecs["reader.proj-1234.platform"].Command
	if want := "reader --api=" + strconv.Itoa(environment.appPort(t, "proj-1234", "internalapi")); readerCommand != want {
		t.Errorf("reader command = %q, want %q: the stack now runs its own internalapi", readerCommand, want)
	}
	if !strings.Contains(environment.output.String(), "restarting reader.proj-1234.platform: this start changed its specification") {
		t.Errorf("output %q, want the line that says why reader restarts", environment.output.String())
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

	if want := []string{"ilot_search-cache_proj-1234_platform"}; !slices.Equal(removed, want) {
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

	if len(removed) != 0 || environment.isApplied("search-cache.proj-1234.platform") {
		t.Errorf("removed %v, applied %t; want the daemon to stop the container with docker stop", removed, environment.isApplied("search-cache.proj-1234.platform"))
	}
}

func TestTheSessionOfTheTerminalReachesAnAppOutsideItsHash(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ShellEnv = func() (map[string]string, map[string]string, error) {
		return map[string]string{"PATH": "/usr/bin"}, map[string]string{"SSH_AUTH_SOCK": "/tmp/agent"}, nil
	}

	environment.start(t, environment.base, StartOptions{}, "standalone")

	spec := environment.supervisor.runningSpecs["standalone.base.platform"]
	if spec.Session["SSH_AUTH_SOCK"] != "/tmp/agent" || spec.Env["SSH_AUTH_SOCK"] != "" {
		t.Errorf("env = %v, session = %v; want the agent in the session part only", spec.Env, spec.Session)
	}
	if len(*environment.buildEnvirons) > 0 && !slices.Contains((*environment.buildEnvirons)[0], "SSH_AUTH_SOCK=/tmp/agent") {
		t.Errorf("build environ = %v, want the agent of the terminal", (*environment.buildEnvirons)[0])
	}
}

// After an SSH reconnect the old SSH_AUTH_SOCK is dead, and only the session differs, so the hash is the same.
func TestARequestedStartOrRestartGivesTheAppTheSessionOfTheNewTerminal(t *testing.T) {
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

	if got := environment.supervisor.runningSpecs["standalone.base.platform"].Session["SSH_AUTH_SOCK"]; got != "/tmp/second-agent" {
		t.Errorf("SSH_AUTH_SOCK after ilot start = %q, want the agent of the new terminal", got)
	}

	environment.executor.ShellEnv = sessionOf("/tmp/third-agent")
	if err := environment.executor.RestartApps(t.Context(), environment.base, []string{"standalone"}); err != nil {
		t.Fatal(err)
	}

	if got := environment.supervisor.runningSpecs["standalone.base.platform"].Session["SSH_AUTH_SOCK"]; got != "/tmp/third-agent" {
		t.Errorf("SSH_AUTH_SOCK after ilot restart = %q, want the agent of the new terminal", got)
	}
}
