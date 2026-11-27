package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

func TestAfterADaemonRestartAStartStartsOnlyWhatItNeeds(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.simulateDaemonRestart()

	environment.start(t, environment.base, StartOptions{}, "standalone")

	if want := []string{"standalone.base.shop"}; !slices.Equal(environment.supervisor.startedInOrder, want) {
		t.Errorf("started %v, want %v only: the other members wait for their own command", environment.supervisor.startedInOrder, want)
	}
}

func TestAfterADaemonRestartListShowsDownAndStartAllRestoresInOrder(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.start(t, environment.base, StartOptions{}, "standalone")
	if err := environment.executor.Stop(t.Context(), environment.base, []string{"standalone"}); err != nil {
		t.Fatal(err)
	}
	environment.supervisor.simulateDaemonRestart()

	listing, err := environment.executor.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if row := rowFor(listing.Apps, "task-1234", "publicapi"); row.Status != "down" {
		t.Errorf("publicapi.task-1234 = %q, want down: the stack owns it and the daemon does not run it", row.Status)
	}
	if row := rowFor(listing.Apps, "base", "standalone"); row.Status != "stopped" {
		t.Errorf("standalone.base = %q, want stopped: no stack owns it", row.Status)
	}
	if listing.Restore != "ilot start --all" {
		t.Errorf("restore = %q, want the command that restores the members", listing.Restore)
	}

	if err := environment.executor.StartAll(t.Context()); err != nil {
		t.Fatal(err)
	}

	started := environment.supervisor.startedInOrder
	for _, processName := range []string{"scheduler.base.shop", "router.task-1234.shop", "internalapi.base.shop", "publicapi.task-1234.shop"} {
		if !slices.Contains(started, processName) {
			t.Errorf("started %v, want %s back", started, processName)
		}
	}
	for _, dependency := range []string{"scheduler.base.shop", "router.task-1234.shop", "internalapi.base.shop"} {
		if slices.Index(started, dependency) > slices.Index(started, "publicapi.task-1234.shop") {
			t.Errorf("started %v, want %s before publicapi.task-1234.shop", started, dependency)
		}
	}
	if slices.Contains(started, "standalone.base.shop") {
		t.Errorf("started %v, want standalone.base.shop left stopped", started)
	}
	listing, err = environment.executor.List(t.Context())
	if err != nil || listing.Restore != "" {
		t.Errorf("restore after ilot start --all = %q, %v; want nothing left down", listing.Restore, err)
	}
}

func TestStartAllSendsNoCallForAMemberThatRuns(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.forgetProcess("publicapi.task-1234.shop")
	environment.supervisor.calls = nil

	if err := environment.executor.StartAll(t.Context()); err != nil {
		t.Fatal(err)
	}

	if want := []string{"put publicapi.task-1234.shop"}; !slices.Equal(environment.supervisor.calls, want) {
		t.Errorf("calls = %v, want %v", environment.supervisor.calls, want)
	}
}

// brokenTemplateInAStack makes members of task-1234 whose run template no longer renders, as after an edit of its ilot.yaml.
func brokenTemplateInAStack(t *testing.T, environment testEnvironment) {
	t.Helper()
	baseConfig := testConfig()
	featureConfig := configWithApps(baseConfig, map[string]config.App{
		"reader":        {Scope: "stack", Run: "reader"},
		"reader-client": {Scope: "stack", Run: "client", DependsOn: []string{"reader"}},
	})
	useConfigPerStack(environment, map[string]config.Config{workspace.BaseStackName: baseConfig, "task-1234": featureConfig})
	environment.executor.Config = featureConfig
	environment.start(t, environment.featureStack, StartOptions{}, "reader-client")
	environment.executor.Config = baseConfig
	environment.start(t, environment.base, StartOptions{}, "standalone")
	featureConfig.Apps["reader"] = config.App{Scope: "stack", Run: "sleep {{.Ports.nope}}"}
	environment.supervisor.simulateDaemonRestart()
}

func checkOnlyTheBrokenStackStaysDown(t *testing.T, environment testEnvironment, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "reader.task-1234.shop does not start") || !strings.Contains(err.Error(), "reader-client.task-1234.shop skipped: it depends on reader.task-1234.shop") {
		t.Errorf("error = %v, want the broken template and its dependent named", err)
	}
	started := environment.supervisor.startedInOrder
	if !slices.Contains(started, "standalone.base.shop") {
		t.Errorf("started %v, want standalone.base.shop back: its stack has no broken template", started)
	}
	if slices.Contains(started, "reader.task-1234.shop") || slices.Contains(started, "reader-client.task-1234.shop") {
		t.Errorf("started %v, want reader and reader-client left down", started)
	}
	if !environment.isStoredAsRunning(t, "task-1234", "reader") {
		t.Error("reader lost its membership, want it kept for the next start")
	}
}

func TestStartAllStartsTheOtherAppsWhenATemplateDoesNotRender(t *testing.T) {
	environment := newTestEnvironment(t)
	brokenTemplateInAStack(t, environment)

	checkOnlyTheBrokenStackStaysDown(t, environment, environment.executor.StartAll(t.Context()))
}

func TestStartAllSkipsABaseAppThatNoConfigDeclaresAnyMore(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Config.Apps["envdump"] = config.App{Run: "env"}
	environment.start(t, environment.base, StartOptions{}, "envdump", "standalone")
	delete(environment.executor.Config.Apps, "envdump")
	environment.supervisor.simulateDaemonRestart()

	listing, err := environment.executor.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if row := rowFor(listing.Apps, "base", "envdump"); !row.Undeclared || listing.Restore != "ilot start --all" {
		t.Errorf("envdump = %+v, restore = %q; want envdump undeclared, and ilot start --all for standalone", row, listing.Restore)
	}
	environment.supervisor.calls = nil

	err = environment.executor.StartAll(t.Context())

	if err != nil {
		t.Errorf("StartAll = %v, want no failure for an undeclared app", err)
	}
	if output := environment.output.String(); !strings.Contains(output, "warning: envdump.base.shop does not start: no ilot.yaml declares it any more") || !strings.Contains(output, "ilot stop envdump") {
		t.Errorf("output %q, want envdump named with the command that removes it", output)
	}
	if slices.Contains(environment.supervisor.calls, "put envdump.base.shop") || !slices.Contains(environment.supervisor.startedInOrder, "standalone.base.shop") {
		t.Errorf("calls = %v, want standalone back and no call for envdump", environment.supervisor.calls)
	}
	listing, err = environment.executor.List(t.Context())
	if err != nil || listing.Restore != "" {
		t.Errorf("restore = %q, %v; want nothing: ilot start --all does not start envdump", listing.Restore, err)
	}
}

func TestAStartWithNoAppStartsTheMembersOfItsStackAndTheirDependencies(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.start(t, environment.base, StartOptions{}, "standalone")
	environment.supervisor.simulateDaemonRestart()

	if err := environment.executor.StartStack(t.Context(), environment.featureStack); err != nil {
		t.Fatal(err)
	}

	started := environment.supervisor.startedInOrder
	for _, processName := range []string{"scheduler.base.shop", "router.task-1234.shop", "internalapi.base.shop", "publicapi.task-1234.shop"} {
		if !slices.Contains(started, processName) {
			t.Errorf("started %v, want %s", started, processName)
		}
	}
	if slices.Contains(started, "standalone.base.shop") {
		t.Errorf("started %v, want standalone.base.shop left down: it is a member of another stack", started)
	}
}

func TestAStartWithNoAppInAStackThatOwnsNoAppSaysSo(t *testing.T) {
	environment := newTestEnvironment(t)

	if err := environment.executor.StartStack(t.Context(), environment.featureStack); err != nil {
		t.Fatal(err)
	}

	if len(environment.supervisor.startedInOrder) != 0 || !strings.Contains(environment.output.String(), "the stack task-1234 owns no app") {
		t.Errorf("started %v, output %q; want nothing started and a line that says why", environment.supervisor.startedInOrder, environment.output.String())
	}
}

func TestStartAllSkipsAStackWhoseWorktreesAreAllDeleted(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.simulateDaemonRestart()
	environment.supervisor.startedInOrder = nil
	environment.executor.CheckoutsForStack = func(string) (map[string]workspace.Worktree, error) {
		return map[string]workspace.Worktree{
			"shop":   {Repo: "shop", Dir: "/src/shop", Branch: "base", IsMain: true},
			"studio": {Repo: "studio", Dir: "/src/studio", Branch: "base", IsMain: true},
		}, nil
	}

	err := environment.executor.StartAll(t.Context())

	if err != nil {
		t.Fatalf("StartAll = %v, want no failure for a stack with a deleted worktree", err)
	}
	for _, processName := range environment.supervisor.startedInOrder {
		if strings.HasSuffix(processName, ".task-1234.shop") {
			t.Errorf("started %s from the main checkout, want the stack left down", processName)
		}
	}
	want := "task-1234 does not start: it has no worktree of its own. ilot clean removes it, or binds it again if its worktree moved"
	if output := environment.output.String(); !strings.Contains(output, want) {
		t.Errorf("output %q, want %q", output, want)
	}
}

func TestANamedStartInAStackWhoseWorktreesAreAllDeletedIsRefused(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.startedInOrder = nil
	environment.executor.CheckoutsForStack = func(string) (map[string]workspace.Worktree, error) {
		return map[string]workspace.Worktree{"shop": {Repo: "shop", Dir: "/src/shop", Branch: "base", IsMain: true}}, nil
	}

	err := environment.executor.Start(t.Context(), environment.featureStack, []string{"standalone"}, StartOptions{})

	want := "task-1234 has no worktree of its own. ilot clean removes it, or binds it again if its worktree moved"
	if err == nil || err.Error() != want {
		t.Errorf("Start = %v, want %q", err, want)
	}
	if len(environment.supervisor.startedInOrder) != 0 {
		t.Errorf("started %v, want nothing from the main checkout", environment.supervisor.startedInOrder)
	}
}
