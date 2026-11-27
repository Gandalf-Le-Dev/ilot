package engine

import (
	"context"
	"errors"
	"testing"
)

func TestACancelAfterTheFirstAppEndsTheStart(t *testing.T) {
	environment := newTestEnvironment(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	environment.supervisor.onWaitReady = func(string) { cancel() }

	err := environment.executor.Start(ctx, environment.base, []string{"publicapi"}, StartOptions{})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Start = %v, want the cancel", err)
	}
	if len(environment.supervisor.calls) != 1 {
		t.Errorf("calls = %v, want one: the first app, and nothing after the cancel", environment.supervisor.calls)
	}
}

func TestACancelAfterTheFirstAppEndsStartAll(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "publicapi")
	environment.supervisor.simulateDaemonRestart()
	environment.supervisor.calls = nil
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	environment.supervisor.onWaitReady = func(string) { cancel() }

	err := environment.executor.StartAll(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("StartAll = %v, want the cancel", err)
	}
	if len(environment.supervisor.calls) != 1 {
		t.Errorf("calls = %v, want one: the first app, and nothing after the cancel", environment.supervisor.calls)
	}
}
