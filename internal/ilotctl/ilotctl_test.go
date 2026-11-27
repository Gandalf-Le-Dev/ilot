package ilotctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/appconfig"
	"github.com/Gandalf-Le-Dev/ilot/internal/checkouts"
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/personal"
	"github.com/Gandalf-Le-Dev/ilot/internal/stack"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

type processBehavior int

const (
	processEndedAfterItWasReady processBehavior = iota + 1
	processEndsAtEveryStart
	processEndsWhileWaitedFor
	processStaysNotReady
)

type fakeSupervisor struct {
	daemonIsUp        bool
	behaviorByProcess map[string]processBehavior
	onWaitReady       func(processName string)
	applyError        error
	routes            []daemon.Route
	events            []string
	runningSpecs      map[string]ProcessSpec
	startedInOrder    []string
	restarted         []string
	waitedReady       []string
	logBeforeReady    map[string]logLineBeforeReady
}

type logLineBeforeReady struct {
	logPath string
	message string
}

func (supervisor *fakeSupervisor) Apply(ctx context.Context, specs []ProcessSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if supervisor.applyError != nil {
		return supervisor.applyError
	}
	if len(specs) == 0 && !supervisor.daemonIsUp {
		return nil
	}
	supervisor.daemonIsUp = true
	nextRunningSpecs := map[string]ProcessSpec{}
	for _, spec := range specs {
		if _, wasRunning := supervisor.runningSpecs[spec.Name]; !wasRunning {
			supervisor.startedInOrder = append(supervisor.startedInOrder, spec.Name)
			supervisor.events = append(supervisor.events, "start "+spec.Name)
			if supervisor.behaviorByProcess[spec.Name] == processEndedAfterItWasReady {
				delete(supervisor.behaviorByProcess, spec.Name)
			}
		}
		nextRunningSpecs[spec.Name] = spec
	}
	supervisor.runningSpecs = nextRunningSpecs
	return nil
}

func (supervisor *fakeSupervisor) SetRoutes(_ context.Context, routes []daemon.Route) error {
	if supervisor.daemonIsUp {
		supervisor.routes = routes
	}
	return nil
}

func (supervisor *fakeSupervisor) routeOf(host string) (daemon.Route, bool) {
	index := slices.IndexFunc(supervisor.routes, func(route daemon.Route) bool { return route.Host == host })
	if index < 0 {
		return daemon.Route{}, false
	}
	return supervisor.routes[index], true
}

func (supervisor *fakeSupervisor) IsUp(context.Context) bool {
	return supervisor.daemonIsUp
}

func (supervisor *fakeSupervisor) Shutdown(context.Context) error {
	supervisor.simulateDaemonRestart()
	supervisor.events = append(supervisor.events, "shutdown")
	return nil
}

func (supervisor *fakeSupervisor) RestartCommand(processName string) string {
	return "restart " + processName
}

func (supervisor *fakeSupervisor) simulateDaemonRestart() {
	supervisor.daemonIsUp = false
	supervisor.runningSpecs = map[string]ProcessSpec{}
	supervisor.startedInOrder = nil
	supervisor.events = nil
}

func (supervisor *fakeSupervisor) setBehavior(processName string, behavior processBehavior) {
	if supervisor.behaviorByProcess == nil {
		supervisor.behaviorByProcess = map[string]processBehavior{}
	}
	supervisor.behaviorByProcess[processName] = behavior
}

func (supervisor *fakeSupervisor) forgetProcess(processName string) {
	delete(supervisor.runningSpecs, processName)
}

func (supervisor *fakeSupervisor) Restart(_ context.Context, processName string) error {
	if _, isKnown := supervisor.runningSpecs[processName]; !isKnown {
		return fmt.Errorf("%s: %w", processName, daemon.ErrUnknownProcess)
	}
	supervisor.restarted = append(supervisor.restarted, processName)
	supervisor.events = append(supervisor.events, "restart "+processName)
	if supervisor.behaviorByProcess[processName] == processEndedAfterItWasReady {
		delete(supervisor.behaviorByProcess, processName)
	}
	return nil
}

func (supervisor *fakeSupervisor) Processes(context.Context) (map[string]daemon.Process, error) {
	processByName := map[string]daemon.Process{}
	for name, spec := range supervisor.runningSpecs {
		process := daemon.Process{Name: name, State: daemon.StateReady, PID: 1, ExitCode: 0, Hash: spec.Hash()}
		switch supervisor.behaviorByProcess[name] {
		case processEndedAfterItWasReady, processEndsAtEveryStart:
			process.State, process.PID, process.ExitCode = daemon.StateExited, 0, 1
		case processStaysNotReady:
			process.State = daemon.StateStarting
		case processEndsWhileWaitedFor:
		}
		processByName[name] = process
	}
	return processByName, nil
}

func (supervisor *fakeSupervisor) WaitReady(ctx context.Context, processName string, timeout time.Duration) error {
	supervisor.waitedReady = append(supervisor.waitedReady, processName)
	supervisor.events = append(supervisor.events, "ready "+processName)
	if line, writesLog := supervisor.logBeforeReady[processName]; writesLog {
		if err := os.MkdirAll(filepath.Dir(line.logPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(line.logPath, []byte(line.message+"\n"), 0o600); err != nil {
			return err
		}
	}
	if supervisor.onWaitReady != nil {
		supervisor.onWaitReady(processName)
	}
	if behavior, hasBehavior := supervisor.behaviorByProcess[processName]; hasBehavior && ctx.Err() == nil {
		switch behavior {
		case processEndedAfterItWasReady, processEndsAtEveryStart, processEndsWhileWaitedFor:
			return &daemon.ExitedError{Name: processName, ExitCode: 1}
		case processStaysNotReady:
			return fmt.Errorf("%w after %s", &daemon.NotReadyError{Name: processName, State: daemon.StateStarting}, timeout)
		}
	}
	return ctx.Err()
}

type recordedBuild struct {
	workingDir string
	command    string
}

type testEnvironment struct {
	controller    *Controller
	supervisor    *fakeSupervisor
	builds        *[]recordedBuild
	buildEnvirons *[][]string
	output        *bytes.Buffer
	master        stack.Worktree
	featureStack  stack.Worktree
}

// Every ilot command builds its own controller.
func (environment testEnvironment) controllerOfANewCommand() *Controller {
	controller := *environment.controller
	return &controller
}

func testConfig() appconfig.Config {
	const buildPlatform = "go build -o {{.BinDir}}/platform ./cmd/platformv2"
	const gatewayAddress = `http://localhost:{{port "gateway" "http"}}`
	return appconfig.Config{
		Domain: "dev.example.net",
		External: map[string]appconfig.ExternalService{
			"keyring":   {Port: 8200, StartHint: "restart devbox"},
			"legacyapi": {Port: 10004, StartHint: "start it by hand"},
		},
		Apps: map[string]appconfig.App{
			"temporal": {
				Shared:     true,
				Autostart:  true,
				Run:        "temporal server start-dev --port {{.Ports.grpc}} --ui-port {{.Ports.ui}}",
				Ports:      []string{"grpc", "ui"},
				FixedPorts: map[string]int{"grpc": 7233, "ui": 10012},
				Route:      "ui",
				Ready:      appconfig.ReadyCheck{Port: "grpc", Path: ""},
			},
			"restapi": {
				Shared:     true,
				Autostart:  true,
				Dir:        "/legacy/restapi",
				Run:        "perl main.pl prefork",
				Ports:      []string{"http"},
				FixedPorts: map[string]int{"http": 10003},
				Route:      "http",
				Watch:      appconfig.Watch{Paths: []string{"."}, Exts: []string{"pl", "pm"}},
			},
			"doc": {
				Shared:     true,
				Run:        "doc/run.sh",
				Ports:      []string{"http"},
				FixedPorts: map[string]int{"http": 10007},
				Route:      "http",
			},
			"gateway": {
				PerStack:   true,
				Autostart:  true,
				Run:        "gatewayd -listen-addr :{{.Ports.http}}",
				Ports:      []string{"http"},
				FixedPorts: map[string]int{"http": 9090},
			},
			"internalapi": {
				Autostart: true,
				Build:     buildPlatform,
				Run:       "{{.BinDir}}/platform api internalapi --port={{.Ports.http}}",
				Env:       map[string]string{"GATEWAY_URL": gatewayAddress, "TEMPORAL_ADDRESS": `localhost:{{port "temporal" "grpc"}}`},
				Ports:     []string{"http"},
				Route:     "http",
				DependsOn: []string{"gateway", "temporal"},
			},
			"publicapi": {
				Build:     buildPlatform,
				Run:       "{{.BinDir}}/platform api publicapi --port={{.Ports.http}}",
				Env:       map[string]string{"GATEWAY_URL": gatewayAddress},
				Ports:     []string{"http"},
				Route:     "http",
				DependsOn: []string{"gateway", "internalapi", "temporal", "keyring"},
			},
			"standalone": {
				Run:   "standalone --port={{.Ports.http}}",
				Ports: []string{"http"},
			},
			"search": {
				PerStack:   true,
				Ports:      []string{"http"},
				FixedPorts: map[string]int{"http": 16271},
				Route:      "http",
				Env:        map[string]string{"GATEWAY_URL": `http://{{host "gateway"}}:{{port "gateway" "http"}}`, "REGION": "DEV1"},
				DependsOn:  []string{"search-cache", "gateway"},
				Container: appconfig.Container{
					Image:         "registry/search:latest",
					Mounts:        map[string]string{"/src/schemas": `{{repo "portal"}}/schemas`},
					EnvFiles:      []string{"/etc/acme.conf.d/devhost.conf"},
					Links:         map[string]string{"postgres-cache": "search-cache"},
					ExternalLinks: map[string]string{"legacy-db": "devhost_legacy_{{.User}}"},
				},
			},
			"search-cache": {
				Ports:      []string{"postgres"},
				FixedPorts: map[string]int{"postgres": 5432},
				Ready:      appconfig.ReadyCheck{Port: "", Path: "", Command: "pg_isready -U postgres"},
				Container:  appconfig.Container{Image: "registry/search-cache:latest"},
			},
			"front-like": {
				Run:       "front --api=http://{{host \"search\"}}:{{port \"search\" \"http\"}} --gw={{host \"gateway\"}}",
				Ports:     []string{"http"},
				DependsOn: []string{"search"},
			},
			"portal-standalone": {
				Repo:  "portal",
				Build: "go build ./cmd/api",
				Run:   "api --port={{.Ports.http}} --from={{.WorktreeDir}}",
				Ports: []string{"http"},
			},
		},
	}
}

func fakeCheckoutsForStack(stackName string) (map[string]checkouts.Checkout, error) {
	checkoutByRepo := map[string]checkouts.Checkout{
		"platform": {Repo: "platform", Dir: "/src/platform", Branch: "master", IsMainCheckout: true},
		"portal":   {Repo: "portal", Dir: "/src/portal", Branch: "master", IsMainCheckout: true},
	}
	if stackName == "proj-1234" {
		checkoutByRepo["platform"] = checkouts.Checkout{Repo: "platform", Dir: "/worktrees/proj-1234", Branch: "PROJ-1234-x", IsMainCheckout: false}
	}
	return checkoutByRepo, nil
}

func newTestEnvironment(t *testing.T) testEnvironment {
	t.Helper()
	supervisor := &fakeSupervisor{runningSpecs: map[string]ProcessSpec{}}
	builds := &[]recordedBuild{}
	buildEnvirons := &[][]string{}
	output := &bytes.Buffer{}
	controller := &Controller{
		Config:     testConfig(),
		Store:      state.Store{RootDir: t.TempDir()},
		Supervisor: supervisor,
		RunBuild: func(_ context.Context, workingDir, shellCommand string, environ []string) error {
			*builds = append(*builds, recordedBuild{workingDir: workingDir, command: shellCommand})
			*buildEnvirons = append(*buildEnvirons, environ)
			return nil
		},
		EnsureImage:         func(context.Context, string) (string, error) { return "", nil },
		IsPortFreeOnHost:    func(int) bool { return true },
		IsExternalServiceUp: func(int) bool { return true },
		CheckoutsForStack:   fakeCheckoutsForStack,
		UsesMise:            func(string) bool { return false },
		BackendHost:         "devbox",
		ReadyTimeout:        time.Second,
		ProgressInterval:    0,
		Output:              output,
	}
	return testEnvironment{
		controller:    controller,
		supervisor:    supervisor,
		builds:        builds,
		buildEnvirons: buildEnvirons,
		output:        output,
		master:        stack.Worktree{Name: "master", WorktreeDir: "/src/platform", MainCheckoutDir: "/src/platform"},
		featureStack:  stack.Worktree{Name: "proj-1234", WorktreeDir: "/worktrees/proj-1234", MainCheckoutDir: "/src/platform"},
	}
}

func (environment testEnvironment) start(t *testing.T, worktree stack.Worktree, options StartOptions, appNames ...string) {
	t.Helper()
	if err := environment.controller.Start(t.Context(), worktree, appNames, options); err != nil {
		t.Fatalf("Start(%s, %v): %v", worktree.Name, appNames, err)
	}
}

func (environment testEnvironment) isStoredAsRunning(t *testing.T, stackName, appName string) bool {
	t.Helper()
	stackState, err := environment.controller.Store.Load(stackName)
	if err != nil {
		t.Fatal(err)
	}
	return stackState.Apps[appName].Running
}

func (environment testEnvironment) isApplied(processName string) bool {
	_, isApplied := environment.supervisor.runningSpecs[processName]
	return isApplied
}

func (environment testEnvironment) appPort(t *testing.T, stackName, appName string) int {
	t.Helper()
	stackState, err := environment.controller.Store.Load(stackName)
	if err != nil {
		t.Fatal(err)
	}
	port := stackState.Apps[appName].Ports["http"]
	if port == 0 {
		t.Fatalf("%s has no http port in stack %s", appName, stackName)
	}
	return port
}

func TestStartInAWorktreeTakesMissingDependenciesFromMaster(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	wantOrder := []string{"gateway.proj-1234", "gateway.master", "temporal.devbox", "internalapi.master", "publicapi.proj-1234"}
	if firstWaits := firstOccurrences(environment.supervisor.waitedReady); !slices.Equal(firstWaits, wantOrder) {
		t.Errorf("waited ready in order %v, want %v", firstWaits, wantOrder)
	}
	if !slices.Equal(environment.supervisor.startedInOrder, wantOrder) {
		t.Errorf("started by the supervisor in order %v, want %v", environment.supervisor.startedInOrder, wantOrder)
	}
}

func TestStartBuildsEachStackOnceInItsOwnWorktree(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	var buildDirs []string
	for _, build := range *environment.builds {
		buildDirs = append(buildDirs, build.workingDir)
	}
	slices.Sort(buildDirs)
	if want := []string{"/src/platform", "/worktrees/proj-1234"}; !slices.Equal(buildDirs, want) {
		t.Errorf("builds ran in %v, want %v", buildDirs, want)
	}
}

func TestEnvironmentTemplatesUseTheStackOwnGatewayAndSharedPorts(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	worktreePublicapi := environment.supervisor.runningSpecs["publicapi.proj-1234"]
	if want := "http://localhost:" + strconv.Itoa(environment.appPort(t, "proj-1234", "gateway")); worktreePublicapi.Env["GATEWAY_URL"] != want {
		t.Errorf("GATEWAY_URL = %q, want %q", worktreePublicapi.Env["GATEWAY_URL"], want)
	}
	if got := environment.supervisor.runningSpecs["internalapi.master"].Env["TEMPORAL_ADDRESS"]; got != "localhost:7233" {
		t.Errorf("TEMPORAL_ADDRESS = %q", got)
	}
	if worktreePublicapi.Dir != "/worktrees/proj-1234" {
		t.Errorf("WorkingDir = %q", worktreePublicapi.Dir)
	}
	wantBinDir := filepath.Join(environment.controller.Store.StackDir("proj-1234"), "bin")
	if !strings.HasPrefix(worktreePublicapi.Command, wantBinDir+"/platform api publicapi --port=") {
		t.Errorf("Command = %q", worktreePublicapi.Command)
	}
}

func TestStartRefusesWhenAnExternalDependencyIsDown(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.IsExternalServiceUp = func(int) bool { return false }

	err := environment.controller.Start(t.Context(), environment.featureStack, []string{"publicapi"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "restart devbox") {
		t.Fatalf("err = %v, want the start hint of keyring", err)
	}
	if len(*environment.builds) != 0 || len(environment.supervisor.startedInOrder) != 0 {
		t.Errorf("built %v and started %v before refusing", *environment.builds, environment.supervisor.startedInOrder)
	}
}

func TestStartWithoutDependenciesStartsOnlyTheRequestedApp(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.master, StartOptions{}, "gateway")

	environment.start(t, environment.master, StartOptions{SkipDependencies: true}, "internalapi")

	if want := []string{"gateway.master", "internalapi.master"}; !slices.Equal(environment.supervisor.startedInOrder, want) {
		t.Errorf("started %v, want %v", environment.supervisor.startedInOrder, want)
	}
}

func TestStartWithoutDependenciesFailsWhenATemplateNeedsAMissingPort(t *testing.T) {
	environment := newTestEnvironment(t)

	err := environment.controller.Start(t.Context(), environment.featureStack, []string{"internalapi"}, StartOptions{SkipDependencies: true})

	if err == nil || !strings.Contains(err.Error(), `"gateway"`) {
		t.Fatalf("err = %v, want an error naming the gateway", err)
	}
}

func TestStartOfARunningAppRebuildsAndRestartsIt(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.master, StartOptions{}, "standalone")
	*environment.builds = nil

	environment.start(t, environment.master, StartOptions{}, "standalone")

	if !slices.Contains(environment.supervisor.restarted, "standalone.master") {
		t.Errorf("restarted %v, want standalone.master", environment.supervisor.restarted)
	}
}

func TestStartShowsTheLogOfAnAppWhileItWaitsForIt(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.logBeforeReady = map[string]logLineBeforeReady{
		"standalone.master": {logPath: filepath.Join(environment.controller.Store.StackDir("master"), "logs", "standalone.log"), message: "listening"},
	}

	environment.start(t, environment.master, StartOptions{}, "standalone")

	if !strings.Contains(environment.output.String(), "standalone.master | listening\n") {
		t.Errorf("output = %q, want the log line of standalone.master", environment.output.String())
	}
}

func TestStartPrintsWhileABuildRuns(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ProgressInterval = 5 * time.Millisecond
	environment.controller.RunBuild = func(context.Context, string, string, []string) error {
		time.Sleep(30 * time.Millisecond)
		return nil
	}

	environment.start(t, environment.master, StartOptions{SkipDependencies: true}, "publicapi")

	if !strings.Contains(environment.output.String(), "still building publicapi.master, ") {
		t.Errorf("output = %q, want a progress line while publicapi.master builds", environment.output.String())
	}
}

func TestStartOfARunningDependencyDoesNotRebuildIt(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.master, StartOptions{}, "internalapi")
	*environment.builds = nil
	environment.supervisor.restarted = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	for _, build := range *environment.builds {
		if build.workingDir == "/src/platform" {
			t.Errorf("rebuilt master although its internalapi already ran: %+v", build)
		}
	}
	if slices.Contains(environment.supervisor.restarted, "internalapi.master") {
		t.Error("restarted master's internalapi")
	}
}

func TestStartRestartsADependencyWhoseProcessEnded(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.master, StartOptions{}, "publicapi")
	environment.supervisor.setBehavior("internalapi.master", processEndedAfterItWasReady)
	environment.supervisor.restarted = nil

	environment.start(t, environment.master, StartOptions{}, "publicapi")

	if !slices.Contains(environment.supervisor.restarted, "internalapi.master") {
		t.Errorf("restarted %v, want internalapi.master, whose process ended", environment.supervisor.restarted)
	}
	if slices.Contains(environment.supervisor.restarted, "temporal.devbox") {
		t.Errorf("restarted %v, want temporal.devbox left alone: its process runs", environment.supervisor.restarted)
	}
}

func TestStartKeepsACrashedDependencyInItsOwnStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.setBehavior("internalapi.master", processEndedAfterItWasReady)
	environment.supervisor.restarted = nil
	environment.supervisor.startedInOrder = nil
	*environment.builds = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if !slices.Contains(environment.supervisor.restarted, "internalapi.master") {
		t.Errorf("restarted %v, want internalapi.master restarted in master", environment.supervisor.restarted)
	}
	if _, isApplied := environment.supervisor.runningSpecs["internalapi.proj-1234"]; isApplied || slices.Contains(environment.supervisor.startedInOrder, "internalapi.proj-1234") {
		t.Errorf("started %v, want the crashed dependency kept in master, not moved to the ticket stack", environment.supervisor.startedInOrder)
	}
	if !slices.ContainsFunc(*environment.builds, func(build recordedBuild) bool { return build.workingDir == "/src/platform" }) {
		t.Errorf("builds = %+v, want master built again before its internalapi restarts", *environment.builds)
	}
}

func TestStartStartsAgainADependencyTheSupervisorDoesNotKnow(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.forgetProcess("internalapi.master")
	environment.supervisor.restarted = nil
	environment.supervisor.startedInOrder = nil
	*environment.builds = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if !slices.Contains(environment.supervisor.startedInOrder, "internalapi.master") {
		t.Errorf("started %v, want internalapi.master given back to the supervisor", environment.supervisor.startedInOrder)
	}
	if slices.Contains(environment.supervisor.restarted, "internalapi.master") {
		t.Errorf("restarted %v, want no restart of a process the supervisor does not know", environment.supervisor.restarted)
	}
	if !slices.ContainsFunc(*environment.builds, func(build recordedBuild) bool { return build.workingDir == "/src/platform" }) {
		t.Errorf("builds = %+v, want master built again", *environment.builds)
	}
}

func TestStartDoesNotRestartARunningDependency(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.restarted = nil
	*environment.builds = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	for _, runningDependency := range []string{"internalapi.master", "temporal.devbox"} {
		if slices.Contains(environment.supervisor.restarted, runningDependency) {
			t.Errorf("restarted %v, want %s left alone: it runs and was not named", environment.supervisor.restarted, runningDependency)
		}
	}
	if !slices.Contains(environment.supervisor.restarted, "publicapi.proj-1234") {
		t.Errorf("restarted %v, want the named publicapi.proj-1234", environment.supervisor.restarted)
	}
	if slices.ContainsFunc(*environment.builds, func(build recordedBuild) bool { return build.workingDir == "/src/platform" }) {
		t.Errorf("builds = %+v, want no build of master: its internalapi runs", *environment.builds)
	}
}

func TestStartStopsTheAppItStartedWhenItsProcessEnds(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.setBehavior("publicapi.master", processEndsAtEveryStart)
	logFile := filepath.Join(environment.controller.Store.StackDir("master"), "logs", "publicapi.log")
	environment.supervisor.logBeforeReady = map[string]logLineBeforeReady{"publicapi.master": {logPath: logFile, message: "pull access denied"}}

	err := environment.controller.Start(t.Context(), environment.master, []string{"publicapi"}, StartOptions{})

	if _, hasEnded := errors.AsType[*daemon.ExitedError](err); !hasEnded || !strings.Contains(err.Error(), "see "+logFile) {
		t.Fatalf("Start = %v, want the ended process and its log %s", err, logFile)
	}
	if environment.isStoredAsRunning(t, "master", "publicapi") || environment.isApplied("publicapi.master") {
		t.Error("publicapi.master ended but stays marked running or in the supervisor")
	}
	if !environment.isStoredAsRunning(t, "master", "internalapi") || !environment.isApplied("internalapi.master") {
		t.Error("internalapi.master started well in the same run but was stopped")
	}
	if _, err := os.Stat(logFile); err != nil {
		t.Errorf("the log of publicapi.master is gone after the cleanup, but the error names it: %v", err)
	}
}

func TestStartStopsANamedRunningAppWhoseRestartEnds(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.master, StartOptions{}, "publicapi")
	environment.supervisor.setBehavior("publicapi.master", processEndsWhileWaitedFor)

	err := environment.controller.Start(t.Context(), environment.master, []string{"publicapi"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "publicapi.master exited with code 1") {
		t.Fatalf("Start = %v, want the failure of publicapi.master", err)
	}
	if environment.isStoredAsRunning(t, "master", "publicapi") || environment.isApplied("publicapi.master") {
		t.Error("the named publicapi.master was restarted by this run and ended, but stays marked running or in the supervisor")
	}
}

func TestACancelDuringTheCleanupDoesNotCutIt(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.setBehavior("publicapi.master", processEndsAtEveryStart)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hasPublicapiEnded := false
	environment.supervisor.onWaitReady = func(processName string) {
		switch {
		case processName == "publicapi.master":
			hasPublicapiEnded = true
		case processName == "gateway.master" && hasPublicapiEnded:
			cancel()
		}
	}

	err := environment.controller.Start(ctx, environment.master, []string{"publicapi"}, StartOptions{})

	if err == nil || strings.Contains(err.Error(), "after its failure") {
		t.Fatalf("Start = %v, want only the failure of publicapi.master", err)
	}
	if environment.isStoredAsRunning(t, "master", "publicapi") || environment.isApplied("publicapi.master") {
		t.Error("a cancel while the cleanup restarted the gateway left publicapi.master half stopped")
	}
}

func TestAFailedCleanupSaysSoAndARetryStillRestartsTheApp(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.setBehavior("standalone.master", processEndsAtEveryStart)
	environment.supervisor.onWaitReady = func(processName string) {
		if processName == "standalone.master" {
			environment.supervisor.applyError = errors.New("the ilot daemon does not answer")
		}
	}

	err := environment.controller.Start(t.Context(), environment.master, []string{"standalone"}, StartOptions{})

	if err == nil {
		t.Fatal("Start = nil, want the failure of standalone.master")
	}
	failureIndex := strings.Index(err.Error(), "standalone.master exited with code 1")
	cleanupIndex := strings.Index(err.Error(), "stop standalone.master after its failure: the ilot daemon does not answer")
	if failureIndex < 0 || cleanupIndex < failureIndex {
		t.Fatalf("Start = %v, want the failure of standalone.master first, then the failed stop", err)
	}
	if environment.isStoredAsRunning(t, "master", "standalone") || !environment.isApplied("standalone.master") {
		t.Fatal("want the half-done cleanup: standalone.master stored as stopped, but still held by the supervisor")
	}

	environment.supervisor.applyError = nil
	environment.supervisor.onWaitReady = nil
	environment.supervisor.setBehavior("standalone.master", processEndedAfterItWasReady)
	environment.supervisor.restarted = nil

	environment.start(t, environment.master, StartOptions{}, "standalone")

	if !slices.Contains(environment.supervisor.restarted, "standalone.master") {
		t.Errorf("restarted %v, want standalone.master: the supervisor still holds its ended process", environment.supervisor.restarted)
	}
	if !environment.isStoredAsRunning(t, "master", "standalone") {
		t.Error("standalone.master is not marked running after the retry")
	}
}

func TestARetryStartsTheFailedAppAgain(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.setBehavior("publicapi.master", processEndsAtEveryStart)
	if err := environment.controller.Start(t.Context(), environment.master, []string{"publicapi"}, StartOptions{}); err == nil {
		t.Fatal("Start = nil, want the failure of publicapi.master")
	}
	delete(environment.supervisor.behaviorByProcess, "publicapi.master")
	environment.supervisor.startedInOrder = nil
	environment.supervisor.restarted = nil
	environment.supervisor.waitedReady = nil

	environment.start(t, environment.master, StartOptions{}, "publicapi")

	if want := []string{"publicapi.master"}; !slices.Equal(environment.supervisor.startedInOrder, want) {
		t.Errorf("started %v, want only %v", environment.supervisor.startedInOrder, want)
	}
	if !slices.Contains(environment.supervisor.waitedReady, "publicapi.master") || !environment.isStoredAsRunning(t, "master", "publicapi") {
		t.Error("publicapi.master is not started and ready after the retry")
	}
	if slices.Contains(environment.supervisor.restarted, "internalapi.master") {
		t.Errorf("restarted %v, want internalapi.master left alone", environment.supervisor.restarted)
	}
}

func TestARetryStartsAFailedDependencyWithoutNamingIt(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.setBehavior("internalapi.master", processEndsAtEveryStart)
	if err := environment.controller.Start(t.Context(), environment.featureStack, []string{"publicapi"}, StartOptions{}); err == nil {
		t.Fatal("Start = nil, want the failure of internalapi.master")
	}
	if environment.isStoredAsRunning(t, "master", "internalapi") || environment.isApplied("internalapi.master") {
		t.Fatal("internalapi.master ended but stays marked running or in the supervisor")
	}
	delete(environment.supervisor.behaviorByProcess, "internalapi.master")
	environment.supervisor.startedInOrder = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if !slices.Contains(environment.supervisor.startedInOrder, "internalapi.master") || !environment.isApplied("publicapi.proj-1234") {
		t.Errorf("started %v, want internalapi.master and publicapi.proj-1234", environment.supervisor.startedInOrder)
	}
	if environment.isApplied("internalapi.proj-1234") {
		t.Error("the retry moved internalapi into the ticket stack")
	}
}

func TestAFailedTicketStackDependencyNamesTheCommandThatStartsItThereAgain(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "internalapi", "publicapi")
	environment.supervisor.setBehavior("internalapi.proj-1234", processEndsAtEveryStart)

	err := environment.controller.Start(t.Context(), environment.featureStack, []string{"publicapi"}, StartOptions{})

	if err == nil || !strings.HasSuffix(err.Error(), "internalapi ran in proj-1234: name it to start it there again: ilot start internalapi publicapi") {
		t.Fatalf("Start = %v, want it to end with the command that starts internalapi in proj-1234 again", err)
	}
	if environment.isStoredAsRunning(t, "proj-1234", "internalapi") || environment.isApplied("internalapi.proj-1234") {
		t.Error("internalapi.proj-1234 ended but stays marked running or in the supervisor")
	}
}

func TestAFailedNamedAppOrMasterDependencyNeedsNoCommandToStartItAgain(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		failedProcess string
		appNames      []string
	}{
		{name: "named app in the ticket stack", failedProcess: "internalapi.proj-1234", appNames: []string{"internalapi", "publicapi"}},
		{name: "dependency in master", failedProcess: "internalapi.master", appNames: []string{"publicapi"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			environment := newTestEnvironment(t)
			environment.supervisor.setBehavior(testCase.failedProcess, processEndsAtEveryStart)

			err := environment.controller.Start(t.Context(), environment.featureStack, testCase.appNames, StartOptions{})

			if err == nil || !strings.Contains(err.Error(), testCase.failedProcess) {
				t.Fatalf("Start = %v, want the failure of %s", err, testCase.failedProcess)
			}
			if strings.Contains(err.Error(), "name it to start it there again") {
				t.Errorf("Start = %v, want no command: a plain retry starts %s again", err, testCase.failedProcess)
			}
		})
	}
}

func TestStartNeverStopsADependencyItOnlyWaitsFor(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.setBehavior("internalapi.master", processEndsWhileWaitedFor)

	err := environment.controller.Start(t.Context(), environment.featureStack, []string{"publicapi"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "internalapi.master") {
		t.Fatalf("Start = %v, want the failure of internalapi.master", err)
	}
	if !environment.isStoredAsRunning(t, "master", "internalapi") || !environment.isApplied("internalapi.master") {
		t.Error("internalapi.master ran before this start, which only waited for it, but it was stopped")
	}
}

func TestStartKeepsAnAppThatTimesOut(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.setBehavior("publicapi.master", processStaysNotReady)

	err := environment.controller.Start(t.Context(), environment.master, []string{"publicapi"}, StartOptions{})

	if err == nil || !strings.HasSuffix(err.Error(), "it still starts: ilot keeps it") {
		t.Fatalf("Start = %v, want it to say that the app still starts", err)
	}
	if !environment.isStoredAsRunning(t, "master", "publicapi") || !environment.isApplied("publicapi.master") {
		t.Error("publicapi.master still starts but was stopped")
	}
}

func TestStartKeepsTheAppWhenTheContextIsCancelled(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		hasEndedProcess bool
	}{
		{name: "process still runs", hasEndedProcess: false},
		{name: "process ends as the wait is cancelled", hasEndedProcess: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			environment := newTestEnvironment(t)
			if testCase.hasEndedProcess {
				environment.supervisor.setBehavior("publicapi.master", processEndsAtEveryStart)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			environment.supervisor.onWaitReady = func(processName string) {
				if processName == "publicapi.master" {
					cancel()
				}
			}

			err := environment.controller.Start(ctx, environment.master, []string{"publicapi"}, StartOptions{})

			if err == nil {
				t.Fatal("Start = nil, want the cancelled wait")
			}
			if !environment.isStoredAsRunning(t, "master", "publicapi") || !environment.isApplied("publicapi.master") {
				t.Error("publicapi.master was stopped after a cancel")
			}
		})
	}
}

func TestPortsStayTheSameAcrossStopAndStart(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.master, StartOptions{}, "standalone")
	portBefore := environment.appPort(t, "master", "standalone")

	if err := environment.controller.Stop(t.Context(), environment.master, []string{"standalone"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	environment.start(t, environment.master, StartOptions{}, "standalone")

	if portAfter := environment.appPort(t, "master", "standalone"); portAfter != portBefore {
		t.Errorf("port moved from %d to %d", portBefore, portAfter)
	}
}

func TestAllocationSkipsPortsOfOtherStacksAndBusyPorts(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.IsPortFreeOnHost = func(port int) bool { return port != appconfig.ReservedPortFirst }
	environment.start(t, environment.master, StartOptions{}, "standalone")
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	masterPort := environment.appPort(t, "master", "standalone")
	worktreePort := environment.appPort(t, "proj-1234", "standalone")
	if masterPort == appconfig.ReservedPortFirst || masterPort == worktreePort {
		t.Errorf("master got %d and the worktree got %d", masterPort, worktreePort)
	}
}

func TestStopRemovesTheProcessAndWarnsAboutDependentsInOtherStacks(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if err := environment.controller.Stop(t.Context(), environment.master, []string{"internalapi"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, isRunning := environment.supervisor.runningSpecs["internalapi.master"]; isRunning {
		t.Error("internalapi.master still runs")
	}
	if !strings.Contains(environment.output.String(), "publicapi.proj-1234") {
		t.Errorf("output %q does not warn about publicapi.proj-1234", environment.output.String())
	}
}

func TestTheProxyGetsARouteForEveryRoutedApp(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	route, isRouted := environment.supervisor.routeOf("publicapi.proj-1234.dev.example.net")
	if !isRouted || route.Port != environment.appPort(t, "proj-1234", "publicapi") {
		t.Errorf("route of publicapi = %+v, routed %t; want its http port. Routes: %+v", route, isRouted, environment.supervisor.routes)
	}
	if want := "cd '/worktrees/proj-1234' && ilot start publicapi"; route.StartHint != want {
		t.Errorf("start hint = %q, want %q", route.StartHint, want)
	}
	if _, isRouted := environment.supervisor.routeOf("gateway.master.dev.example.net"); isRouted {
		t.Error("gateway.master has a route, but the app declares none")
	}
}

func TestAStoppedAppKeepsItsRoute(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi", "standalone")

	if err := environment.controller.Stop(t.Context(), environment.featureStack, []string{"publicapi"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, isRouted := environment.supervisor.routeOf("publicapi.proj-1234.dev.example.net"); !isRouted {
		t.Errorf("routes after the stop = %+v, want the route of publicapi kept: its URL then says how to start it", environment.supervisor.routes)
	}
}

func TestListShowsAppsWithLiveStatusAndURL(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ProxyPort = "18080"
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	listing, err := environment.controller.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	publicapiRow := rowFor(listing.Apps, "proj-1234", "publicapi")
	if publicapiRow.Status != "ready" || publicapiRow.URL != "http://publicapi.proj-1234.dev.example.net:18080" {
		t.Errorf("publicapi row = %+v", publicapiRow)
	}
	if publicapiRow.Ports["http"] != environment.appPort(t, "proj-1234", "publicapi") {
		t.Errorf("publicapi ports = %v, want the http port of its state", publicapiRow.Ports)
	}
	if temporalRow := rowFor(listing.Apps, "devbox", "temporal"); temporalRow.Status != "ready" || temporalRow.URL != "http://temporal.devbox.dev.example.net:18080" {
		t.Errorf("temporal row = %+v", temporalRow)
	}
	wantExternalServices := []ExternalServiceRow{{Name: "keyring", Status: "up", Port: 8200}, {Name: "legacyapi", Status: "up", Port: 10004}}
	if !slices.Equal(listing.ExternalServices, wantExternalServices) {
		t.Errorf("external services = %+v, want %+v", listing.ExternalServices, wantExternalServices)
	}
}

func firstOccurrences(names []string) []string {
	var firstSeen []string
	for _, name := range names {
		if !slices.Contains(firstSeen, name) {
			firstSeen = append(firstSeen, name)
		}
	}
	return firstSeen
}

func rowFor(rows []AppRow, stackName, appName string) AppRow {
	for _, row := range rows {
		if row.Stack == stackName && row.App == appName {
			return row
		}
	}
	return AppRow{}
}

func TestSharedAppRunsOnceInTheDevboxStackWithItsFixedPorts(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "doc")
	environment.start(t, environment.master, StartOptions{}, "doc")

	if want := []string{"doc.devbox"}; !slices.Equal(environment.supervisor.startedInOrder, want) {
		t.Errorf("started %v, want %v", environment.supervisor.startedInOrder, want)
	}
	if port := environment.appPort(t, "devbox", "doc"); port != 10007 {
		t.Errorf("doc port = %d, want its fixed 10007", port)
	}
}

func TestMasterGatewayUsesItsFixedPortAndWorktreeGatewaysDoNot(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if port := environment.appPort(t, "master", "gateway"); port != 9090 {
		t.Errorf("master gateway port = %d, want 9090", port)
	}
	if port := environment.appPort(t, "proj-1234", "gateway"); port < appconfig.ReservedPortFirst || port > appconfig.ReservedPortLast {
		t.Errorf("worktree gateway port = %d, want one from the reserved range", port)
	}
}

func TestStartRefusesAFixedPortHeldByAnotherProcess(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.IsPortFreeOnHost = func(port int) bool { return port != 9090 }

	err := environment.controller.Start(t.Context(), environment.master, []string{"gateway"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "9090") {
		t.Fatalf("err = %v, want an error naming port 9090", err)
	}
	if len(environment.supervisor.startedInOrder) != 0 {
		t.Errorf("started %v before refusing", environment.supervisor.startedInOrder)
	}
}

func TestAppDirIsItsWorkingDirectory(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "restapi", "standalone")

	if got := environment.supervisor.runningSpecs["restapi.devbox"].Dir; got != "/legacy/restapi" {
		t.Errorf("restapi WorkingDir = %q", got)
	}
	if got := environment.supervisor.runningSpecs["standalone.proj-1234"].Dir; got != "/worktrees/proj-1234" {
		t.Errorf("standalone WorkingDir = %q", got)
	}
}

func TestWatchedAppRunsWithAWatcherThatRestartsIt(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.master, StartOptions{}, "restapi")

	watcher, isRunning := environment.supervisor.runningSpecs["restapi-watch.devbox"]
	if !isRunning {
		t.Fatalf("no watcher among %v", slices.Sorted(maps.Keys(environment.supervisor.runningSpecs)))
	}
	for _, wantPart := range []string{"watchexec --postpone", "--watch '/legacy/restapi'", "--exts pl,pm", "'restart restapi.devbox'"} {
		if !strings.Contains(watcher.Command, wantPart) {
			t.Errorf("watcher command %q lacks %q", watcher.Command, wantPart)
		}
	}

	if err := environment.controller.Stop(t.Context(), environment.master, []string{"restapi"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, isRunning := environment.supervisor.runningSpecs["restapi-watch.devbox"]; isRunning {
		t.Error("the watcher outlives its app")
	}
}

func TestWatchedAppWithABuildRebuildsBeforeTheRestart(t *testing.T) {
	environment := newTestEnvironment(t)
	internalapi := environment.controller.Config.Apps["internalapi"]
	internalapi.Watch = appconfig.Watch{Paths: []string{"."}, Exts: []string{"go"}}
	environment.controller.Config.Apps["internalapi"] = internalapi

	environment.start(t, environment.featureStack, StartOptions{}, "internalapi")

	watcher, isRunning := environment.supervisor.runningSpecs["internalapi-watch.proj-1234"]
	if !isRunning {
		t.Fatalf("no watcher among %v", slices.Sorted(maps.Keys(environment.supervisor.runningSpecs)))
	}
	binDir := filepath.Join(environment.controller.Store.StackDir("proj-1234"), "bin")
	lockFile := filepath.Join(environment.controller.Store.StackDir("proj-1234"), "build.lock")
	for _, wantPart := range []string{
		"--watch '/worktrees/proj-1234'",
		"--exts go",
		"flock ",
		lockFile,
		"cd ",
		"go build -o " + binDir + "/platform ./cmd/platformv2",
		"&& restart internalapi.proj-1234",
	} {
		if !strings.Contains(watcher.Command, wantPart) {
			t.Errorf("watcher command %q lacks %q", watcher.Command, wantPart)
		}
	}
	if strings.Index(watcher.Command, "go build") > strings.Index(watcher.Command, "restart internalapi") {
		t.Errorf("watcher command %q restarts before it builds", watcher.Command)
	}
}

func TestASharedAppHasItsRouteInTheDevboxStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.master, StartOptions{}, "temporal")

	if route, isRouted := environment.supervisor.routeOf("temporal.devbox.dev.example.net"); !isRouted || route.Port != 10012 {
		t.Errorf("route of temporal = %+v, routed %t; want the temporal UI on port 10012", route, isRouted)
	}
}

func TestBootStartsAutostartAppsAndTheAppsThatWereRunning(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	environment.supervisor.simulateDaemonRestart()

	if err := environment.controller.Boot(t.Context(), environment.master); err != nil {
		t.Fatalf("Boot: %v", err)
	}

	running := slices.Sorted(maps.Keys(environment.supervisor.runningSpecs))
	want := []string{"gateway.master", "internalapi.master", "restapi-watch.devbox", "restapi.devbox", "standalone.proj-1234", "temporal.devbox"}
	if !slices.Equal(running, want) {
		t.Errorf("running after boot %v, want %v", running, want)
	}
	if temporalIndex, apiIndex := slices.Index(environment.supervisor.startedInOrder, "temporal.devbox"), slices.Index(environment.supervisor.startedInOrder, "internalapi.master"); temporalIndex > apiIndex {
		t.Errorf("started %v: want temporal before internalapi", environment.supervisor.startedInOrder)
	}
}

func TestBootDoesNothingWhenTheDaemonRuns(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.master, StartOptions{}, "standalone")
	environment.supervisor.startedInOrder = nil

	if err := environment.controller.Boot(t.Context(), environment.master); err != nil {
		t.Fatalf("Boot: %v", err)
	}

	if len(environment.supervisor.startedInOrder) != 0 {
		t.Errorf("Boot started %v although the daemon already ran", environment.supervisor.startedInOrder)
	}
}

func TestBootBuildsOnlyStacksWithoutBinaries(t *testing.T) {
	environment := newTestEnvironment(t)
	masterBinDir := filepath.Join(environment.controller.Store.StackDir("master"), "bin")
	if err := os.MkdirAll(masterBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(masterBinDir, "platform"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := environment.controller.Boot(t.Context(), environment.master); err != nil {
		t.Fatalf("Boot: %v", err)
	}

	if len(*environment.builds) != 0 {
		t.Errorf("Boot rebuilt %v although master has its binary", *environment.builds)
	}
}

func TestBootStopsAFailedAppAndStartsTheOthers(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.master, StartOptions{}, "publicapi")
	environment.supervisor.simulateDaemonRestart()
	environment.supervisor.setBehavior("internalapi.master", processEndsAtEveryStart)

	err := environment.controller.Boot(t.Context(), environment.master)

	if err == nil || !strings.Contains(err.Error(), "internalapi.master") {
		t.Fatalf("Boot = %v, want an error naming internalapi.master", err)
	}
	if !strings.Contains(err.Error(), "publicapi.master") {
		t.Errorf("Boot = %v, want it to name publicapi.master as skipped", err)
	}
	started := environment.supervisor.startedInOrder
	for _, wantStarted := range []string{"temporal.devbox", "restapi.devbox", "gateway.master"} {
		if !slices.Contains(started, wantStarted) {
			t.Errorf("started %v, want %s despite the failure", started, wantStarted)
		}
	}
	if slices.Contains(started, "publicapi.master") {
		t.Errorf("started %v, want publicapi.master skipped because internalapi.master failed", started)
	}
	if environment.isStoredAsRunning(t, "master", "internalapi") || environment.isApplied("internalapi.master") {
		t.Error("internalapi.master failed at boot but stays marked running or in the supervisor")
	}
	if environment.isStoredAsRunning(t, "master", "publicapi") {
		t.Error("publicapi.master was skipped at boot but is marked running")
	}
	if !environment.isStoredAsRunning(t, "devbox", "temporal") || !environment.isApplied("temporal.devbox") {
		t.Error("temporal.devbox started well at boot but is not kept running")
	}
}

func TestBootSkipsAnAppWhoseDependencyFailedInTheTicketStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "internalapi", "publicapi")
	environment.supervisor.simulateDaemonRestart()
	environment.supervisor.setBehavior("internalapi.proj-1234", processEndsAtEveryStart)

	err := environment.controller.Boot(t.Context(), environment.master)

	if err == nil || !strings.Contains(err.Error(), "publicapi.proj-1234 skipped: it depends on internalapi.proj-1234") {
		t.Fatalf("Boot = %v, want publicapi.proj-1234 skipped because internalapi.proj-1234 failed", err)
	}
	if slices.Contains(environment.supervisor.startedInOrder, "publicapi.proj-1234") {
		t.Errorf("started %v, want publicapi.proj-1234 skipped", environment.supervisor.startedInOrder)
	}
}

func TestBootSkipsAContainerWhoseLinkedContainerImageFailed(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")
	environment.supervisor.simulateDaemonRestart()
	environment.controller.EnsureImage = (&imageRecorder{failing: map[string]string{"registry/search-cache:latest": "unauthorized"}}).ensure

	err := environment.controller.Boot(t.Context(), environment.master)

	if err == nil || !strings.Contains(err.Error(), "search.proj-1234 skipped: it depends on search-cache.proj-1234") {
		t.Fatalf("Boot = %v, want search.proj-1234 skipped because the image of search-cache.proj-1234 failed", err)
	}
	if slices.Contains(environment.supervisor.startedInOrder, "search.proj-1234") {
		t.Errorf("started %v, want search.proj-1234 skipped", environment.supervisor.startedInOrder)
	}
}

func TestHomeURLIsTheRouteListOfTheProxy(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ProxyPort = "18080"

	if got := environment.controller.HomeURL(); got != "http://dev.example.net:18080" {
		t.Errorf("HomeURL = %q", got)
	}
}

func TestTheRouteOfAnAppCarriesItsHomePath(t *testing.T) {
	environment := newTestEnvironment(t)
	publicapi := environment.controller.Config.Apps["publicapi"]
	publicapi.HomePath = "/swagger/"
	environment.controller.Config.Apps["publicapi"] = publicapi

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if route, _ := environment.supervisor.routeOf("publicapi.proj-1234.dev.example.net"); route.HomePath != "/swagger/" {
		t.Errorf("route of publicapi = %+v, want the home path /swagger/", route)
	}
}

func TestListShowsTheHomePathInTheURL(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ProxyPort = "18080"
	publicapi := environment.controller.Config.Apps["publicapi"]
	publicapi.HomePath = "/swagger/"
	environment.controller.Config.Apps["publicapi"] = publicapi
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	listing, err := environment.controller.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if url := rowFor(listing.Apps, "proj-1234", "publicapi").URL; url != "http://publicapi.proj-1234.dev.example.net:18080/swagger/" {
		t.Errorf("publicapi URL = %q", url)
	}
}

func TestLogFileOfAnAppIsInTheStackThatRunsIt(t *testing.T) {
	environment := newTestEnvironment(t)

	worktreeLog, err := environment.controller.LogFile(environment.featureStack, "publicapi")
	if err != nil {
		t.Fatalf("LogFile(publicapi): %v", err)
	}
	if want := filepath.Join(environment.controller.Store.StackDir("proj-1234"), "logs", "publicapi.log"); worktreeLog != want {
		t.Errorf("publicapi log = %q, want %q", worktreeLog, want)
	}
	sharedLog, err := environment.controller.LogFile(environment.featureStack, "temporal")
	if err != nil {
		t.Fatalf("LogFile(temporal): %v", err)
	}
	if want := filepath.Join(environment.controller.Store.StackDir("devbox"), "logs", "temporal.log"); sharedLog != want {
		t.Errorf("temporal log = %q, want %q", sharedLog, want)
	}
	if _, err := environment.controller.LogFile(environment.featureStack, "nope"); err == nil {
		t.Error("want an error for an unknown app")
	}
}

func TestAppRunsFromTheCheckoutOfItsRepo(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "portal-standalone", "standalone")

	portalSpec := environment.supervisor.runningSpecs["portal-standalone.proj-1234"]
	if portalSpec.Dir != "/src/portal" || !strings.HasSuffix(portalSpec.Command, "--from=/src/portal") {
		t.Errorf("portal-standalone spec = %+v, want portal's main checkout", portalSpec)
	}
	if got := environment.supervisor.runningSpecs["standalone.proj-1234"].Dir; got != "/worktrees/proj-1234" {
		t.Errorf("standalone WorkingDir = %q, want platform's worktree", got)
	}
	if !slices.Contains(*environment.builds, recordedBuild{workingDir: "/src/portal", command: "go build ./cmd/api"}) {
		t.Errorf("builds = %+v, want the portal build in portal's checkout", *environment.builds)
	}
}

func environmentOf(t *testing.T, environment testEnvironment, appName string) map[string]EnvEntry {
	t.Helper()
	entries, err := environment.controller.Environment(environment.featureStack, appName)
	if err != nil {
		t.Fatalf("Environment(%s): %v", appName, err)
	}
	entryByKey := map[string]EnvEntry{}
	for _, entry := range entries {
		entryByKey[entry.Key] = entry
	}
	return entryByKey
}

func TestEnvironmentAppliesTheSourcesInOrder(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.Login = "jdoe"
	environment.controller.Config.RepoEnv = map[string]map[string]string{"platform": {"LOG_LEVEL": "info", "REGION": "DEV1"}}
	standalone := environment.controller.Config.Apps["standalone"]
	standalone.Env = map[string]string{"PORT": "{{.Ports.http}}", "REGION": "EU1"}
	environment.controller.Config.Apps["standalone"] = standalone
	environment.controller.Personal = personal.Settings{
		Env:   map[string]string{"LOG_LEVEL": "debug", "PORT": "1"},
		Apps:  map[string]map[string]string{"standalone": {"API_TOKEN": "custom"}},
		Repos: nil,
	}
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	entryByKey := environmentOf(t, environment, "standalone")

	wantPort := strconv.Itoa(environment.appPort(t, "proj-1234", "standalone"))
	want := map[string]EnvEntry{
		"LOG_LEVEL": {Key: "LOG_LEVEL", Value: "debug", Source: "personal"},
		"REGION":    {Key: "REGION", Value: "EU1", Source: "app"},
		"PORT":      {Key: "PORT", Value: wantPort, Source: "computed"},
		"API_TOKEN": {Key: "API_TOKEN", Value: "custom", Source: "personal app"},
	}
	for key, wantEntry := range want {
		if entryByKey[key] != wantEntry {
			t.Errorf("%s = %+v, want %+v", key, entryByKey[key], wantEntry)
		}
	}
	if environment.supervisor.runningSpecs["standalone.proj-1234"].Env["LOG_LEVEL"] != "debug" {
		t.Errorf("the process does not get the environment: %v", environment.supervisor.runningSpecs["standalone.proj-1234"].Env)
	}
}

func TestTemplatesKnowTheStackTheUserRepoAndURL(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.Login = "jdoe"
	environment.controller.ProxyPort = "18080"
	standalone := environment.controller.Config.Apps["standalone"]
	standalone.Run = `run {{.Stack}} {{.User}} {{repo "portal"}} {{url "temporal"}} {{url "internalapi"}}`
	environment.controller.Config.Apps["standalone"] = standalone

	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	want := "run proj-1234 jdoe /src/portal http://temporal.devbox.dev.example.net:18080 http://internalapi.master.dev.example.net:18080"
	if got := environment.supervisor.runningSpecs["standalone.proj-1234"].Command; got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

func TestContainerCommandRemovesALeftoverContainerFirst(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")

	command := environment.supervisor.runningSpecs["search.proj-1234"].Command
	if !strings.HasPrefix(command, "docker rm -f 'ilot-proj-1234-search' >/dev/null 2>&1 || true && exec docker run --rm --name 'ilot-proj-1234-search'") {
		t.Errorf("command = %q", command)
	}
}

func TestAContainerBuildRunsBeforeItsContainerStarts(t *testing.T) {
	environment := newTestEnvironment(t)
	search := environment.controller.Config.Apps["search"]
	search.Build = "prepare-files"
	environment.controller.Config.Apps["search"] = search

	environment.start(t, environment.featureStack, StartOptions{}, "search")

	var ranPrepare bool
	for _, build := range *environment.builds {
		ranPrepare = ranPrepare || strings.Contains(build.command, "prepare-files")
	}
	if !ranPrepare {
		t.Errorf("builds %v lack the container build", *environment.builds)
	}
}

func TestContainerCommandRunsOnDevproxyWithEnvMountsAndLinks(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.Login = "jdoe"
	environment.start(t, environment.featureStack, StartOptions{}, "search")

	command := environment.supervisor.runningSpecs["search.proj-1234"].Command
	gatewayPort := environment.appPort(t, "proj-1234", "gateway")
	for _, wantPart := range []string{
		"--network ilotproxy",
		"--env-file '/etc/acme.conf.d/devhost.conf'",
		"-e 'REGION'",
		"-e 'GATEWAY_URL'",
		"-v '/src/portal/schemas:/src/schemas'",
		"--link 'ilot-proj-1234-search-cache:postgres-cache'",
		"--link 'devhost_legacy_jdoe:legacy-db'",
		" 'registry/search:latest'",
	} {
		if !strings.Contains(command, wantPart) {
			t.Errorf("command %q lacks %q", command, wantPart)
		}
	}
	if !strings.HasSuffix(command, "'registry/search:latest'") {
		t.Errorf("command %q does not end with the image", command)
	}
	if strings.Contains(command, "DEV1") {
		t.Errorf("command %q shows an env value, which ps would show to every user", command)
	}
	searchEnv := environment.supervisor.runningSpecs["search.proj-1234"].Env
	if searchEnv["REGION"] != "DEV1" || searchEnv["GATEWAY_URL"] != "http://devbox:"+strconv.Itoa(gatewayPort) {
		t.Errorf("search env = %v, want the values for docker run -e to read", searchEnv)
	}
	if order := environment.supervisor.startedInOrder; slices.Index(order, "search-cache.proj-1234") > slices.Index(order, "search.proj-1234") {
		t.Errorf("started %v: want the cache before search", order)
	}
}

func TestContainerProbeUsesTheContainerName(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")

	if probe := environment.supervisor.runningSpecs["search.proj-1234"].Ready; probe.Host != "ilot-proj-1234-search" || probe.Port != 16271 {
		t.Errorf("search probe = %+v", probe)
	}
	if probe := environment.supervisor.runningSpecs["search-cache.proj-1234"].Ready; probe.Command != "docker exec 'ilot-proj-1234-search-cache' sh -c 'pg_isready -U postgres'" {
		t.Errorf("cache probe = %+v", probe)
	}
}

func TestHostTemplateDependsOnTheCaller(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "front-like")

	searchPort := 16271
	want := "front --api=http://ilot-proj-1234-search:" + strconv.Itoa(searchPort) + " --gw=localhost"
	if got := environment.supervisor.runningSpecs["front-like.proj-1234"].Command; got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

func TestContainerFixedPortsAreNotCheckedOnTheHost(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.IsPortFreeOnHost = func(port int) bool { return port != 5432 && port != 16271 }

	environment.start(t, environment.featureStack, StartOptions{}, "search")

	if port := environment.appPort(t, "proj-1234", "search"); port != 16271 {
		t.Errorf("search port = %d, want its fixed 16271 in a worktree stack", port)
	}
}

func TestDependencyWithoutPerStackComesFromMaster(t *testing.T) {
	environment := newTestEnvironment(t)
	search := environment.controller.Config.Apps["search"]
	search.PerStack = false
	environment.controller.Config.Apps["search"] = search

	environment.start(t, environment.featureStack, StartOptions{}, "front-like")

	if want := "front --api=http://ilot-master-search:16271 --gw=localhost"; environment.supervisor.runningSpecs["front-like.proj-1234"].Command != want {
		t.Errorf("Command = %q, want %q", environment.supervisor.runningSpecs["front-like.proj-1234"].Command, want)
	}
}

func TestDomainTemplateGivesTheProxyDomain(t *testing.T) {
	environment := newTestEnvironment(t)
	standalone := environment.controller.Config.Apps["standalone"]
	standalone.Env = map[string]string{"ALLOWED_HOSTS": ".{{.Domain}}"}
	environment.controller.Config.Apps["standalone"] = standalone

	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	if got := environment.supervisor.runningSpecs["standalone.proj-1234"].Env["ALLOWED_HOSTS"]; got != ".dev.example.net" {
		t.Errorf("ALLOWED_HOSTS = %q, want .dev.example.net", got)
	}
}

func TestProxyPortTemplateGivesTheProxyPort(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ProxyPort = "18080"
	standalone := environment.controller.Config.Apps["standalone"]
	standalone.Env = map[string]string{"EXPOSED_PORT": "{{.ProxyPort}}"}
	environment.controller.Config.Apps["standalone"] = standalone

	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	if got := environment.supervisor.runningSpecs["standalone.proj-1234"].Env["EXPOSED_PORT"]; got != "18080" {
		t.Errorf("EXPOSED_PORT = %q, want 18080", got)
	}
}

func TestAnAppWithAStopCommandStopsWithItRendered(t *testing.T) {
	environment := newTestEnvironment(t)
	standalone := environment.controller.Config.Apps["standalone"]
	standalone.Run = "docker run --rm --name standalone-{{.Stack}} -p {{.Ports.http}}:80 nginx"
	standalone.Stop = "docker stop standalone-{{.Stack}}"
	environment.controller.Config.Apps["standalone"] = standalone

	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	if got, want := environment.supervisor.runningSpecs["standalone.proj-1234"].Stop, "docker stop standalone-proj-1234"; got != want {
		t.Errorf("Stop = %q, want %q", got, want)
	}
}

func TestAContainerStopsWithDockerStopAndAProcessWithItsSignal(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "search")

	if got := environment.supervisor.runningSpecs["search-cache.proj-1234"].Stop; got != "docker stop --time 10 'ilot-proj-1234-search-cache'" {
		t.Errorf("search-cache Stop = %q", got)
	}
	if got := environment.supervisor.runningSpecs["gateway.proj-1234"].Stop; got != "" {
		t.Errorf("gateway Stop = %q, want none", got)
	}
}

func TestAnUnknownAppNamesTheReposThatAreNotCloned(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.ReposNotCloned = map[string]string{"portal": "/home/user/go/src/portal"}

	err := environment.controller.Start(t.Context(), environment.featureStack, []string{"portal-front"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "repos.portal.path") || !strings.Contains(err.Error(), "/home/user/go/src/portal") {
		t.Fatalf("Start = %v, want an error naming the repo that is not cloned", err)
	}
}

func TestPersonalEnvReachesAContainerOnlyThroughItsAppSettings(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.Personal = personal.Settings{
		Env:   map[string]string{"DEBUG_TOKEN": "secret"},
		Apps:  map[string]map[string]string{"search": {"FEATURE_FLAG": "true"}},
		Repos: nil,
	}

	environment.start(t, environment.featureStack, StartOptions{}, "search")

	searchSpec := environment.supervisor.runningSpecs["search.proj-1234"]
	if strings.Contains(searchSpec.Command, "DEBUG_TOKEN") || searchSpec.Env["DEBUG_TOKEN"] != "" {
		t.Errorf("search gets the personal env: command %q, env %v", searchSpec.Command, searchSpec.Env)
	}
	if !strings.Contains(searchSpec.Command, "-e 'FEATURE_FLAG'") || searchSpec.Env["FEATURE_FLAG"] != "true" {
		t.Errorf("search lacks its personal app setting: command %q, env %v", searchSpec.Command, searchSpec.Env)
	}
}

func TestRepoEnvWinsOverTheInheritedShellValue(t *testing.T) {
	t.Setenv("LOGKIT_LOGJSON", "from-shell")
	environment := newTestEnvironment(t)
	environment.controller.Config.RepoEnv = map[string]map[string]string{appconfig.DefaultRepo: {"LOGKIT_LOGJSON": "1"}}
	environment.start(t, environment.master, StartOptions{}, "standalone")
	if got := environment.supervisor.runningSpecs["standalone.master"].Env["LOGKIT_LOGJSON"]; got != "1" {
		t.Errorf("LOGKIT_LOGJSON = %q, want the repo value", got)
	}
}

type imageRecorder struct {
	checked []string
	failing map[string]string
}

func (recorder *imageRecorder) ensure(_ context.Context, image string) (string, error) {
	recorder.checked = append(recorder.checked, image)
	if output, isFailing := recorder.failing[image]; isFailing {
		return output, errors.New("exit status 1")
	}
	return "", nil
}

func TestStartChecksImagesBeforeAnyBuild(t *testing.T) {
	environment := newTestEnvironment(t)
	recorder := &imageRecorder{failing: map[string]string{"registry/search-cache:latest": "unauthorized"}}
	environment.controller.EnsureImage = recorder.ensure

	err := environment.controller.Start(t.Context(), environment.featureStack, []string{"search", "publicapi"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "pull registry/search-cache:latest") || !strings.Contains(err.Error(), "docker login") {
		t.Fatalf("Start = %v, want the image and the login hint", err)
	}
	if len(*environment.builds) != 0 {
		t.Errorf("builds ran before the image check failed: %v", *environment.builds)
	}
	if len(environment.supervisor.runningSpecs) != 0 {
		t.Errorf("processes started although an image failed: %v", environment.supervisor.runningSpecs)
	}
}

func TestStartPullsAnImageOnceForTwoApps(t *testing.T) {
	environment := newTestEnvironment(t)
	search := environment.controller.Config.Apps["search"]
	search.Container.Image = "registry/search-cache:latest"
	environment.controller.Config.Apps["search"] = search
	recorder := &imageRecorder{failing: nil}
	environment.controller.EnsureImage = recorder.ensure

	environment.start(t, environment.featureStack, StartOptions{}, "search")

	if want := []string{"registry/search-cache:latest"}; !slices.Equal(recorder.checked, want) {
		t.Errorf("images checked %v, want %v", recorder.checked, want)
	}
}

func TestStartChecksNoImageForARunningContainer(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")
	recorder := &imageRecorder{failing: nil}
	environment.controller.EnsureImage = recorder.ensure

	environment.start(t, environment.featureStack, StartOptions{}, "search")

	if len(recorder.checked) != 0 {
		t.Errorf("images checked %v, want none for containers that already run", recorder.checked)
	}
}

func TestBootSkipsTheAppWhoseImageFailsAndStartsTheOthers(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.master, StartOptions{}, "front-like")
	environment.supervisor.simulateDaemonRestart()
	environment.controller.EnsureImage = (&imageRecorder{failing: map[string]string{"registry/search:latest": "unauthorized"}}).ensure

	err := environment.controller.Boot(t.Context(), environment.master)

	if err == nil || !strings.Contains(err.Error(), "search.master: pull registry/search:latest") || !strings.Contains(err.Error(), "front-like.master skipped") {
		t.Fatalf("Boot = %v, want the failed image named with its app and stack, and front-like.master skipped", err)
	}
	started := environment.supervisor.startedInOrder
	if !slices.Contains(started, "temporal.devbox") || !slices.Contains(started, "gateway.master") {
		t.Errorf("started %v, want the other apps started", started)
	}
	if slices.Contains(started, "search.master") || slices.Contains(started, "front-like.master") {
		t.Errorf("started %v, want search and front-like skipped", started)
	}
}

func TestStartWithNoContainerChecksNoImage(t *testing.T) {
	environment := newTestEnvironment(t)
	recorder := &imageRecorder{failing: nil}
	environment.controller.EnsureImage = recorder.ensure

	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	if len(recorder.checked) != 0 {
		t.Errorf("images checked %v, want none without a container", recorder.checked)
	}
}

func fixedShellEnv(valueByKey map[string]string) func() (map[string]string, error) {
	return func() (map[string]string, error) { return valueByKey, nil }
}
