package engine

import (
	"slices"
	"testing"
)

func TestAfterADaemonRestartAStartStartsOnlyWhatItNeeds(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.simulateDaemonRestart()

	environment.start(t, environment.base, StartOptions{}, "standalone")

	if want := []string{"standalone.base.platform"}; !slices.Equal(environment.supervisor.startedInOrder, want) {
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
	if row := rowFor(listing.Apps, "proj-1234", "publicapi"); row.Status != "down" {
		t.Errorf("publicapi.proj-1234 = %q, want down: the stack owns it and the daemon does not run it", row.Status)
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
	for _, processName := range []string{"temporal.base.platform", "gateway.proj-1234.platform", "internalapi.base.platform", "publicapi.proj-1234.platform"} {
		if !slices.Contains(started, processName) {
			t.Errorf("started %v, want %s back", started, processName)
		}
	}
	for _, dependency := range []string{"temporal.base.platform", "gateway.proj-1234.platform", "internalapi.base.platform"} {
		if slices.Index(started, dependency) > slices.Index(started, "publicapi.proj-1234.platform") {
			t.Errorf("started %v, want %s before publicapi.proj-1234.platform", started, dependency)
		}
	}
	if slices.Contains(started, "standalone.base.platform") {
		t.Errorf("started %v, want standalone.base.platform left stopped", started)
	}
	listing, err = environment.executor.List(t.Context())
	if err != nil || listing.Restore != "" {
		t.Errorf("restore after ilot start --all = %q, %v; want nothing left down", listing.Restore, err)
	}
}

func TestStartAllSendsNoCallForAMemberThatRuns(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.forgetProcess("publicapi.proj-1234.platform")
	environment.supervisor.calls = nil

	if err := environment.executor.StartAll(t.Context()); err != nil {
		t.Fatal(err)
	}

	if want := []string{"put publicapi.proj-1234.platform"}; !slices.Equal(environment.supervisor.calls, want) {
		t.Errorf("calls = %v, want %v", environment.supervisor.calls, want)
	}
}
