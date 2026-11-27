package ilotctl

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedOutput struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (output *lockedOutput) Write(content []byte) (int, error) {
	output.mutex.Lock()
	defer output.mutex.Unlock()
	return output.buffer.Write(content)
}

func (output *lockedOutput) String() string {
	output.mutex.Lock()
	defer output.mutex.Unlock()
	return output.buffer.String()
}

func TestASecondBuildOfAStackWaitsForTheBuildLock(t *testing.T) {
	environment := newTestEnvironment(t)
	output := &lockedOutput{}
	environment.controller.Output = output
	buildStarted := make(chan struct{}, 1)
	environment.controller.RunBuild = func(context.Context, string, string, []string) error {
		buildStarted <- struct{}{}
		return nil
	}
	releaseFirstBuild, err := environment.controller.lockBuilds(t.Context(), "master")
	if err != nil {
		t.Fatalf("lockBuilds: %v", err)
	}

	started := make(chan error, 1)
	go func() {
		started <- environment.controller.Start(t.Context(), environment.master, []string{"publicapi"}, StartOptions{SkipDependencies: true})
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(output.String(), "waiting for another build of stack master") {
		if time.Now().After(deadline) {
			t.Fatalf("output = %q, want a line that says that the build waits", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-buildStarted:
		t.Fatal("the second build ran while the first one held the lock")
	case <-time.After(3 * buildLockPollInterval):
	}

	if err := releaseFirstBuild(); err != nil {
		t.Fatal(err)
	}
	if err := <-started; err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-buildStarted:
	default:
		t.Fatal("the second build never ran after the lock was free")
	}
}

func TestABuildInAnotherStackDoesNotWait(t *testing.T) {
	environment := newTestEnvironment(t)
	releaseOtherStack, err := environment.controller.lockBuilds(t.Context(), "proj-1234")
	if err != nil {
		t.Fatalf("lockBuilds: %v", err)
	}
	defer releaseOtherStack()

	environment.start(t, environment.master, StartOptions{SkipDependencies: true}, "publicapi")

	if len(*environment.builds) != 1 || strings.Contains(environment.output.String(), "waiting for another build") {
		t.Errorf("builds = %+v, output = %q; want one build with no wait", *environment.builds, environment.output.String())
	}
}

func TestACancelEndsTheWaitForTheBuildLock(t *testing.T) {
	environment := newTestEnvironment(t)
	releaseFirstBuild, err := environment.controller.lockBuilds(t.Context(), "master")
	if err != nil {
		t.Fatalf("lockBuilds: %v", err)
	}
	defer releaseFirstBuild()
	ctx, cancel := context.WithTimeout(t.Context(), 2*buildLockPollInterval)
	defer cancel()

	if _, err := environment.controller.lockBuilds(ctx, "master"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lockBuilds = %v, want the end of the context", err)
	}
}
