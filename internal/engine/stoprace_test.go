package engine

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
)

// withSlowApp declares an app with a build, and runs the given command of another terminal while that build runs.
func withSlowApp(environment testEnvironment, duringBuild func(other *Executor) error) {
	environment.executor.Config.Apps["slow"] = config.App{Build: "sleep 6", Run: "slow"}
	other := environment.executorOfANewCommand()
	environment.executor.RunBuild = func(context.Context, string, string, []string) error {
		return duringBuild(other)
	}
}

func TestAStopDuringTheBuildOfAStartKeepsTheAppDown(t *testing.T) {
	environment := newTestEnvironment(t)
	withSlowApp(environment, func(other *Executor) error {
		return other.Stop(t.Context(), environment.base, []string{"slow"})
	})

	environment.start(t, environment.base, StartOptions{}, "slow")

	if environment.isApplied("slow.base.shop") {
		t.Error("slow.base.shop runs, but ilot stop took its membership away during the build")
	}
	if !strings.Contains(environment.output.String(), "slow.base.shop does not start: another ilot command stopped it") {
		t.Errorf("output %q, want the line that says why slow does not start", environment.output.String())
	}
}

func TestARemoveDuringTheBuildOfAStartKeepsTheAppDown(t *testing.T) {
	environment := newTestEnvironment(t)
	withSlowApp(environment, func(other *Executor) error {
		return other.Remove(t.Context(), environment.featureStack)
	})

	environment.start(t, environment.featureStack, StartOptions{SkipDependencies: true}, "slow")

	if environment.isApplied("slow.proj-1234.shop") {
		t.Error("slow.proj-1234.shop runs, but ilot rm removed its stack during the build")
	}
}

func TestAStopDuringTheBuildOfARestartLeavesTheAppStopped(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Config.Apps["slow"] = config.App{Build: "sleep 6", Run: "slow"}
	environment.start(t, environment.base, StartOptions{}, "slow")
	other := environment.executorOfANewCommand()
	environment.executor.RunBuild = func(context.Context, string, string, []string) error {
		return other.Stop(t.Context(), environment.base, []string{"slow"})
	}
	environment.supervisor.restarted = nil

	if err := environment.executor.RestartApps(t.Context(), environment.base, []string{"slow"}); err != nil {
		t.Fatal(err)
	}

	if environment.isApplied("slow.base.shop") || len(environment.supervisor.restarted) != 0 {
		t.Errorf("applied %t, restarted %v; want slow left stopped", environment.isApplied("slow.base.shop"), environment.supervisor.restarted)
	}
}

func TestAStopDuringThePutOfAStartStopsTheApp(t *testing.T) {
	environment := newTestEnvironment(t)
	other := environment.executorOfANewCommand()
	environment.supervisor.onPut = func(processName string) {
		if processName != "standalone.base.shop" {
			return
		}
		environment.supervisor.onPut = nil
		if err := other.Stop(t.Context(), environment.base, []string{"standalone"}); err != nil {
			t.Error(err)
		}
	}

	environment.start(t, environment.base, StartOptions{}, "standalone")

	if environment.isApplied("standalone.base.shop") {
		t.Error("standalone.base.shop runs, but ilot stop took its membership away while the daemon started it")
	}
}

func TestStopStopsAProcessThatRunsWithoutItsMembership(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "standalone")
	current, err := environment.executor.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	stack := current.Stack(environment.executor.Workspace, environment.base)
	app := stack.Apps["standalone"]
	app.Wanted = false
	stack.Apps["standalone"] = app
	if err := environment.executor.Store.Save(current); err != nil {
		t.Fatal(err)
	}
	environment.output.Reset()

	if err := environment.executor.Stop(t.Context(), environment.base, []string{"standalone"}); err != nil {
		t.Fatal(err)
	}

	if environment.isApplied("standalone.base.shop") || strings.Contains(environment.output.String(), "is not running") {
		t.Errorf("applied %t, output %q; want the process stopped", environment.isApplied("standalone.base.shop"), environment.output.String())
	}
}

func TestStopStackStopsTheMembersOfTheStackOnly(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.calls = nil

	if err := environment.executor.StopStack(t.Context(), environment.featureStack); err != nil {
		t.Fatal(err)
	}

	for _, stoppedName := range []string{"publicapi.proj-1234.shop", "gateway.proj-1234.shop"} {
		if !slices.Contains(environment.supervisor.calls, "delete "+stoppedName) {
			t.Errorf("calls = %v, want %s stopped", environment.supervisor.calls, stoppedName)
		}
	}
	if slices.ContainsFunc(environment.supervisor.calls, func(call string) bool { return strings.HasSuffix(call, ".base.shop") }) {
		t.Errorf("calls = %v, want no call for the base stack", environment.supervisor.calls)
	}
	if environment.isStoredAsRunning(t, environment.featureStack, "publicapi") {
		t.Error("publicapi.proj-1234.shop is still a member")
	}
}
