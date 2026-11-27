package engine

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

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
	"github.com/Gandalf-Le-Dev/ilot/internal/ports"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
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
	// Called when a Put arrives, before the fake holds the process.
	onPut    func(processName string)
	putError error
	routes   []daemon.Route
	// Every call that names a process, in order.
	calls          []string
	events         []string
	runningSpecs   map[string]ProcessSpec
	startedInOrder []string
	restarted      []string
	waitedReady    []string
	logBeforeReady map[string]logLineBeforeReady
}

type logLineBeforeReady struct {
	logPath string
	message string
}

func (supervisor *fakeSupervisor) Put(ctx context.Context, spec ProcessSpec) error {
	supervisor.calls = append(supervisor.calls, "put "+spec.Name)
	if err := ctx.Err(); err != nil {
		return err
	}
	if supervisor.onPut != nil {
		supervisor.onPut(spec.Name)
	}
	if supervisor.putError != nil {
		return supervisor.putError
	}
	supervisor.daemonIsUp = true
	running, isKnown := supervisor.runningSpecs[spec.Name]
	hasEnded := supervisor.behaviorByProcess[spec.Name] == processEndedAfterItWasReady || supervisor.behaviorByProcess[spec.Name] == processEndsAtEveryStart
	supervisor.runningSpecs[spec.Name] = spec
	if isKnown && !hasEnded && running.Hash() == spec.Hash() {
		return nil
	}
	supervisor.startedInOrder = append(supervisor.startedInOrder, spec.Name)
	supervisor.events = append(supervisor.events, "start "+spec.Name)
	if supervisor.behaviorByProcess[spec.Name] == processEndedAfterItWasReady {
		delete(supervisor.behaviorByProcess, spec.Name)
	}
	return nil
}

func (supervisor *fakeSupervisor) Up(context.Context) error {
	supervisor.daemonIsUp = true
	return nil
}

func (supervisor *fakeSupervisor) Delete(_ context.Context, processName string) error {
	supervisor.calls = append(supervisor.calls, "delete "+processName)
	if _, isKnown := supervisor.runningSpecs[processName]; isKnown {
		supervisor.events = append(supervisor.events, "delete "+processName)
	}
	delete(supervisor.runningSpecs, processName)
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

func (supervisor *fakeSupervisor) Restart(_ context.Context, spec ProcessSpec) error {
	processName := spec.Name
	if _, isKnown := supervisor.runningSpecs[processName]; !isKnown {
		return fmt.Errorf("%s: %w", processName, daemon.ErrUnknownProcess)
	}
	supervisor.runningSpecs[processName] = spec
	supervisor.calls = append(supervisor.calls, "restart "+processName)
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
	executor      *Executor
	supervisor    *fakeSupervisor
	builds        *[]recordedBuild
	buildEnvirons *[][]string
	output        *bytes.Buffer
	base          string
	featureStack  string
}

// Every ilot command builds its own executor.
func (environment testEnvironment) executorOfANewCommand() *Executor {
	executor := *environment.executor
	return &executor
}

func testConfig() config.Config {
	const buildPlatform = "go build -o {{.BinDir}}/platform ./cmd/platformv2"
	const gatewayAddress = `http://localhost:{{port "gateway" "http"}}`
	return config.Config{
		External: map[string]config.ExternalService{
			"keyring":   {Port: 8200, StartHint: "restart base"},
			"legacyapi": {Port: 10004, StartHint: "start it by hand"},
		},
		Apps: map[string]config.App{
			"temporal": {
				Scope:      "base",
				Autostart:  true,
				Run:        "temporal server start-dev --port {{.Ports.grpc}} --ui-port {{.Ports.ui}}",
				Ports:      []string{"grpc", "ui"},
				FixedPorts: map[string]int{"grpc": 7233, "ui": 10012},
				Route:      "ui",
				Ready:      config.ReadyCheck{Port: "grpc", Path: ""},
			},
			"restapi": {
				Scope:      "base",
				Autostart:  true,
				Dir:        "/legacy/restapi",
				Run:        "perl main.pl prefork",
				Ports:      []string{"http"},
				FixedPorts: map[string]int{"http": 10003},
				Route:      "http",
				Watch:      config.Watch{Paths: []string{"."}, Exts: []string{"pl", "pm"}},
			},
			"doc": {
				Scope:      "base",
				Run:        "doc/run.sh",
				Ports:      []string{"http"},
				FixedPorts: map[string]int{"http": 10007},
				Route:      "http",
			},
			"gateway": {
				Scope:      "stack",
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
				Scope: "stack",
				Route: "http",
				Env: map[string]string{
					"GATEWAY_URL": `http://{{host "gateway"}}:{{port "gateway" "http"}}`,
					"CACHE_URL":   `postgres://{{host "search-cache"}}:{{port "search-cache"}}/cache`,
					"REGION":      "DEV1",
				},
				DependsOn: []string{"search-cache", "gateway"},
				Container: config.Container{
					Image:    "registry/search:latest",
					Ports:    map[string]int{"http": 16271},
					Mounts:   map[string]string{`{{repo "portal"}}/schemas`: "/src/schemas"},
					EnvFiles: []string{"/etc/acme.conf.d/search.conf"},
					Args:     []string{"--shm-size=1g", "--label=stack={{.Stack}}"},
				},
			},
			"search-cache": {
				Scope:     "stack",
				Ready:     config.ReadyCheck{Port: "", Path: "", Command: "pg_isready -U postgres"},
				Container: config.Container{Image: "registry/search-cache:latest", Ports: map[string]int{"postgres": 5432}},
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

func fakeCheckoutsForStack(stackName string) (map[string]workspace.Worktree, error) {
	checkoutByRepo := map[string]workspace.Worktree{
		"platform": {Repo: "platform", Dir: "/src/platform", Branch: "base", IsMain: true},
		"portal":   {Repo: "portal", Dir: "/src/portal", Branch: "base", IsMain: true},
	}
	if stackName == "proj-1234" {
		checkoutByRepo["platform"] = workspace.Worktree{Repo: "platform", Dir: "/worktrees/proj-1234", Branch: "PROJ-1234-x", IsMain: false}
	}
	return checkoutByRepo, nil
}

func newTestEnvironment(t *testing.T) testEnvironment {
	t.Helper()
	supervisor := &fakeSupervisor{runningSpecs: map[string]ProcessSpec{}}
	builds := &[]recordedBuild{}
	buildEnvirons := &[][]string{}
	output := &bytes.Buffer{}
	executor := &Executor{
		Source: Source{
			Workspace:         "platform",
			Root:              "/src/platform",
			Config:            testConfig(),
			Store:             state.Store{RootDir: t.TempDir(), LockTimeout: 0, OnLockWait: nil},
			CheckoutsForStack: fakeCheckoutsForStack,
			ConfigForStack:    nil,
			ReposNotCloned:    nil,
			Settings:          config.WorkspaceSettings{Repos: nil, Env: nil, Apps: nil},
			Machine:           plan.Machine{StateDir: "", Domain: "dev.example.net", ProxyPort: "", Login: "", ShellEnv: nil, SessionEnv: nil, IlotBinary: "/opt/ilot/bin/ilot", IlotEnv: nil},
			ShellEnv:          nil,
		},
		Supervisor: supervisor,
		RunBuild: func(_ context.Context, workingDir, shellCommand string, environ []string) error {
			*builds = append(*builds, recordedBuild{workingDir: workingDir, command: shellCommand})
			*buildEnvirons = append(*buildEnvirons, environ)
			return nil
		},
		EnsureImage:         func(context.Context, string) (string, error) { return "", nil },
		IsPortFreeOnHost:    func(int) bool { return true },
		IsExternalServiceUp: func(int) bool { return true },
		PortRange:           ports.Range{First: 20000, Last: 29999},
		ReadyTimeout:        time.Second,
		ProgressInterval:    0,
		Output:              output,
	}
	executor.Machine.StateDir = executor.Store.RootDir
	return testEnvironment{
		executor:      executor,
		supervisor:    supervisor,
		builds:        builds,
		buildEnvirons: buildEnvirons,
		output:        output,
		base:          "base",
		featureStack:  "proj-1234",
	}
}

func (environment testEnvironment) start(t *testing.T, stackName string, options StartOptions, appNames ...string) {
	t.Helper()
	if err := environment.executor.Start(t.Context(), stackName, appNames, options); err != nil {
		t.Fatalf("Start(%s, %v): %v", stackName, appNames, err)
	}
}

func (environment testEnvironment) storedApp(t *testing.T, stackName, appName string) state.App {
	t.Helper()
	current, err := environment.executor.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return current.App(environment.executor.Workspace, stackName, appName)
}

func (environment testEnvironment) isStoredAsRunning(t *testing.T, stackName, appName string) bool {
	t.Helper()
	return environment.storedApp(t, stackName, appName).Wanted
}

func (environment testEnvironment) isApplied(processName string) bool {
	_, isApplied := environment.supervisor.runningSpecs[processName]
	return isApplied
}

func (environment testEnvironment) appPort(t *testing.T, stackName, appName string) int {
	t.Helper()
	port := environment.storedApp(t, stackName, appName).Ports["http"]
	if port == 0 {
		t.Fatalf("%s has no http port in stack %s", appName, stackName)
	}
	return port
}

func TestStartInAWorktreeTakesMissingDependenciesFromBase(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	wantOrder := []string{"gateway.proj-1234.platform", "gateway.base.platform", "temporal.base.platform", "internalapi.base.platform", "publicapi.proj-1234.platform"}
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

func TestEnvironmentTemplatesUseTheGatewayOfTheStackAndThePortsOfTheBaseStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	worktreePublicapi := environment.supervisor.runningSpecs["publicapi.proj-1234.platform"]
	if want := "http://localhost:" + strconv.Itoa(environment.appPort(t, "proj-1234", "gateway")); worktreePublicapi.Env["GATEWAY_URL"] != want {
		t.Errorf("GATEWAY_URL = %q, want %q", worktreePublicapi.Env["GATEWAY_URL"], want)
	}
	if got := environment.supervisor.runningSpecs["internalapi.base.platform"].Env["TEMPORAL_ADDRESS"]; got != "localhost:7233" {
		t.Errorf("TEMPORAL_ADDRESS = %q", got)
	}
	if worktreePublicapi.Dir != "/worktrees/proj-1234" {
		t.Errorf("WorkingDir = %q", worktreePublicapi.Dir)
	}
	wantBinDir := filepath.Join(environment.executor.StackDir("proj-1234"), "bin")
	if !strings.HasPrefix(worktreePublicapi.Command, wantBinDir+"/platform api publicapi --port=") {
		t.Errorf("Command = %q", worktreePublicapi.Command)
	}
}

func TestStartRefusesWhenAnExternalDependencyIsDown(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.IsExternalServiceUp = func(int) bool { return false }

	err := environment.executor.Start(t.Context(), environment.featureStack, []string{"publicapi"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "restart base") {
		t.Fatalf("err = %v, want the start hint of keyring", err)
	}
	if len(*environment.builds) != 0 || len(environment.supervisor.startedInOrder) != 0 {
		t.Errorf("built %v and started %v before refusing", *environment.builds, environment.supervisor.startedInOrder)
	}
}

func TestStartWithoutDependenciesStartsOnlyTheRequestedApp(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "gateway")

	environment.start(t, environment.base, StartOptions{SkipDependencies: true}, "internalapi")

	if want := []string{"gateway.base.platform", "internalapi.base.platform"}; !slices.Equal(environment.supervisor.startedInOrder, want) {
		t.Errorf("started %v, want %v", environment.supervisor.startedInOrder, want)
	}
}

func TestStartWithoutDependenciesFailsWhenATemplateNeedsAMissingPort(t *testing.T) {
	environment := newTestEnvironment(t)

	err := environment.executor.Start(t.Context(), environment.featureStack, []string{"internalapi"}, StartOptions{SkipDependencies: true})

	if err == nil || !strings.Contains(err.Error(), `"gateway"`) {
		t.Fatalf("err = %v, want an error naming the gateway", err)
	}
}

func TestStartOfARunningAppRebuildsAndRestartsIt(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "standalone")
	*environment.builds = nil

	environment.start(t, environment.base, StartOptions{}, "standalone")

	if !slices.Contains(environment.supervisor.restarted, "standalone.base.platform") {
		t.Errorf("restarted %v, want standalone.base.platform", environment.supervisor.restarted)
	}
}

func TestStartShowsTheLogOfAnAppWhileItWaitsForIt(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.logBeforeReady = map[string]logLineBeforeReady{
		"standalone.base.platform": {logPath: filepath.Join(environment.executor.StackDir("base"), "logs", "standalone.log"), message: "listening"},
	}

	environment.start(t, environment.base, StartOptions{}, "standalone")

	if !strings.Contains(environment.output.String(), "standalone.base.platform | listening\n") {
		t.Errorf("output = %q, want the log line of standalone.base.platform", environment.output.String())
	}
}

func TestStartPrintsWhileABuildRuns(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ProgressInterval = 5 * time.Millisecond
	environment.executor.RunBuild = func(context.Context, string, string, []string) error {
		time.Sleep(30 * time.Millisecond)
		return nil
	}

	environment.start(t, environment.base, StartOptions{SkipDependencies: true}, "publicapi")

	if !strings.Contains(environment.output.String(), "still building publicapi.base.platform, ") {
		t.Errorf("output = %q, want a progress line while publicapi.base.platform builds", environment.output.String())
	}
}

func TestStartOfARunningDependencyDoesNotRebuildIt(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "internalapi")
	*environment.builds = nil
	environment.supervisor.restarted = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	for _, build := range *environment.builds {
		if build.workingDir == "/src/platform" {
			t.Errorf("rebuilt base although its internalapi already ran: %+v", build)
		}
	}
	if slices.Contains(environment.supervisor.restarted, "internalapi.base.platform") {
		t.Error("restarted base's internalapi")
	}
}

func TestStartStartsAgainADependencyWhoseProcessEnded(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "publicapi")
	environment.supervisor.setBehavior("internalapi.base.platform", processEndedAfterItWasReady)
	environment.supervisor.startedInOrder = nil

	environment.start(t, environment.base, StartOptions{}, "publicapi")

	if !slices.Contains(environment.supervisor.startedInOrder, "internalapi.base.platform") {
		t.Errorf("started %v, want internalapi.base.platform, whose process ended", environment.supervisor.startedInOrder)
	}
	if slices.Contains(environment.supervisor.startedInOrder, "temporal.base.platform") || slices.Contains(environment.supervisor.restarted, "temporal.base.platform") {
		t.Errorf("started %v, want temporal.base.platform left alone: its process runs", environment.supervisor.startedInOrder)
	}
}

func TestStartKeepsACrashedDependencyInItsOwnStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.setBehavior("internalapi.base.platform", processEndedAfterItWasReady)
	environment.supervisor.restarted = nil
	environment.supervisor.startedInOrder = nil
	*environment.builds = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if !slices.Contains(environment.supervisor.startedInOrder, "internalapi.base.platform") {
		t.Errorf("started %v, want internalapi.base.platform started again in base", environment.supervisor.startedInOrder)
	}
	if _, isApplied := environment.supervisor.runningSpecs["internalapi.proj-1234.platform"]; isApplied || slices.Contains(environment.supervisor.startedInOrder, "internalapi.proj-1234.platform") {
		t.Errorf("started %v, want the crashed dependency kept in base, not moved to the ticket stack", environment.supervisor.startedInOrder)
	}
	if !slices.ContainsFunc(*environment.builds, func(build recordedBuild) bool { return build.workingDir == "/src/platform" }) {
		t.Errorf("builds = %+v, want base built again before its internalapi restarts", *environment.builds)
	}
}

func TestStartStartsAgainADependencyTheSupervisorDoesNotKnow(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.forgetProcess("internalapi.base.platform")
	environment.supervisor.restarted = nil
	environment.supervisor.startedInOrder = nil
	*environment.builds = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if !slices.Contains(environment.supervisor.startedInOrder, "internalapi.base.platform") {
		t.Errorf("started %v, want internalapi.base.platform given back to the supervisor", environment.supervisor.startedInOrder)
	}
	if slices.Contains(environment.supervisor.restarted, "internalapi.base.platform") {
		t.Errorf("restarted %v, want no restart of a process the supervisor does not know", environment.supervisor.restarted)
	}
	if !slices.ContainsFunc(*environment.builds, func(build recordedBuild) bool { return build.workingDir == "/src/platform" }) {
		t.Errorf("builds = %+v, want base built again", *environment.builds)
	}
}

func TestStartDoesNotRestartARunningDependency(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.restarted = nil
	*environment.builds = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	for _, runningDependency := range []string{"internalapi.base.platform", "temporal.base.platform"} {
		if slices.Contains(environment.supervisor.restarted, runningDependency) {
			t.Errorf("restarted %v, want %s left alone: it runs and was not named", environment.supervisor.restarted, runningDependency)
		}
	}
	if !slices.Contains(environment.supervisor.restarted, "publicapi.proj-1234.platform") {
		t.Errorf("restarted %v, want the named publicapi.proj-1234.platform", environment.supervisor.restarted)
	}
	if slices.ContainsFunc(*environment.builds, func(build recordedBuild) bool { return build.workingDir == "/src/platform" }) {
		t.Errorf("builds = %+v, want no build of base: its internalapi runs", *environment.builds)
	}
}

func TestAFailedStartKeepsTheAppAMemberWithItsLog(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.setBehavior("publicapi.base.platform", processEndsAtEveryStart)
	logFile := filepath.Join(environment.executor.StackDir("base"), "logs", "publicapi.log")
	environment.supervisor.logBeforeReady = map[string]logLineBeforeReady{"publicapi.base.platform": {logPath: logFile, message: "pull access denied"}}

	err := environment.executor.Start(t.Context(), environment.base, []string{"publicapi"}, StartOptions{})

	if _, hasEnded := errors.AsType[*daemon.ExitedError](err); !hasEnded || !strings.Contains(err.Error(), "see "+logFile) {
		t.Fatalf("Start = %v, want the ended process and its log %s", err, logFile)
	}
	if !environment.isStoredAsRunning(t, "base", "publicapi") || !environment.isApplied("publicapi.base.platform") {
		t.Error("publicapi.base.platform ended, and it must stay a member that the daemon holds as ended: a failed start does not change ownership")
	}
	if !environment.isStoredAsRunning(t, "base", "internalapi") || !environment.isApplied("internalapi.base.platform") {
		t.Error("internalapi.base.platform started well in the same run but was stopped")
	}
	if _, err := os.Stat(logFile); err != nil {
		t.Errorf("the log of publicapi.base.platform is gone, but the error names it: %v", err)
	}
}

func TestARetryStartsTheFailedAppAgain(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.setBehavior("publicapi.base.platform", processEndsAtEveryStart)
	if err := environment.executor.Start(t.Context(), environment.base, []string{"publicapi"}, StartOptions{}); err == nil {
		t.Fatal("Start = nil, want the failure of publicapi.base.platform")
	}
	environment.supervisor.setBehavior("publicapi.base.platform", processEndedAfterItWasReady)
	environment.supervisor.startedInOrder = nil
	environment.supervisor.restarted = nil
	environment.supervisor.waitedReady = nil

	environment.start(t, environment.base, StartOptions{}, "publicapi")

	if want := []string{"publicapi.base.platform"}; !slices.Equal(environment.supervisor.startedInOrder, want) {
		t.Errorf("started %v, want only %v", environment.supervisor.startedInOrder, want)
	}
	if !slices.Contains(environment.supervisor.waitedReady, "publicapi.base.platform") || !environment.isStoredAsRunning(t, "base", "publicapi") {
		t.Error("publicapi.base.platform is not started and ready after the retry")
	}
	if slices.Contains(environment.supervisor.restarted, "internalapi.base.platform") {
		t.Errorf("restarted %v, want internalapi.base.platform left alone", environment.supervisor.restarted)
	}
}

func TestARetryStartsAFailedDependencyWithoutNamingIt(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.setBehavior("internalapi.base.platform", processEndsAtEveryStart)
	if err := environment.executor.Start(t.Context(), environment.featureStack, []string{"publicapi"}, StartOptions{}); err == nil {
		t.Fatal("Start = nil, want the failure of internalapi.base.platform")
	}
	environment.supervisor.setBehavior("internalapi.base.platform", processEndedAfterItWasReady)
	environment.supervisor.startedInOrder = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if !slices.Contains(environment.supervisor.startedInOrder, "internalapi.base.platform") || !environment.isApplied("publicapi.proj-1234.platform") {
		t.Errorf("started %v, want internalapi.base.platform and publicapi.proj-1234.platform", environment.supervisor.startedInOrder)
	}
	if environment.isApplied("internalapi.proj-1234.platform") {
		t.Error("the retry moved internalapi into the ticket stack")
	}
}

func TestARetryStartsAFailedTicketStackDependencyInItsStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "internalapi", "publicapi")
	environment.supervisor.setBehavior("internalapi.proj-1234.platform", processEndsAtEveryStart)
	if err := environment.executor.Start(t.Context(), environment.featureStack, []string{"publicapi"}, StartOptions{}); err == nil {
		t.Fatal("Start = nil, want the failure of internalapi.proj-1234.platform")
	}
	environment.supervisor.setBehavior("internalapi.proj-1234.platform", processEndedAfterItWasReady)
	environment.supervisor.startedInOrder = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if !slices.Contains(environment.supervisor.startedInOrder, "internalapi.proj-1234.platform") || slices.Contains(environment.supervisor.startedInOrder, "internalapi.base.platform") {
		t.Errorf("started %v, want internalapi started again in proj-1234, which owns it", environment.supervisor.startedInOrder)
	}
}

func TestAFailedNamedAppOrBaseDependencyNeedsNoCommandToStartItAgain(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		failedProcess string
		appNames      []string
	}{
		{name: "named app in the ticket stack", failedProcess: "internalapi.proj-1234.platform", appNames: []string{"internalapi", "publicapi"}},
		{name: "dependency in base", failedProcess: "internalapi.base.platform", appNames: []string{"publicapi"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			environment := newTestEnvironment(t)
			environment.supervisor.setBehavior(testCase.failedProcess, processEndsAtEveryStart)

			err := environment.executor.Start(t.Context(), environment.featureStack, testCase.appNames, StartOptions{})

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
	environment.supervisor.setBehavior("internalapi.base.platform", processEndsWhileWaitedFor)

	err := environment.executor.Start(t.Context(), environment.featureStack, []string{"publicapi"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "internalapi.base.platform") {
		t.Fatalf("Start = %v, want the failure of internalapi.base.platform", err)
	}
	if !environment.isStoredAsRunning(t, "base", "internalapi") || !environment.isApplied("internalapi.base.platform") {
		t.Error("internalapi.base.platform ran before this start, which only waited for it, but it was stopped")
	}
}

func TestStartKeepsAnAppThatTimesOut(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.setBehavior("publicapi.base.platform", processStaysNotReady)

	err := environment.executor.Start(t.Context(), environment.base, []string{"publicapi"}, StartOptions{})

	if err == nil || !strings.HasSuffix(err.Error(), "it still starts: ilot keeps it") {
		t.Fatalf("Start = %v, want it to say that the app still starts", err)
	}
	if !environment.isStoredAsRunning(t, "base", "publicapi") || !environment.isApplied("publicapi.base.platform") {
		t.Error("publicapi.base.platform still starts but was stopped")
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
				environment.supervisor.setBehavior("publicapi.base.platform", processEndsAtEveryStart)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			environment.supervisor.onWaitReady = func(processName string) {
				if processName == "publicapi.base.platform" {
					cancel()
				}
			}

			err := environment.executor.Start(ctx, environment.base, []string{"publicapi"}, StartOptions{})

			if err == nil {
				t.Fatal("Start = nil, want the cancelled wait")
			}
			if !environment.isStoredAsRunning(t, "base", "publicapi") || !environment.isApplied("publicapi.base.platform") {
				t.Error("publicapi.base.platform was stopped after a cancel")
			}
		})
	}
}

func TestPortsStayTheSameAcrossStopAndStart(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "standalone")
	portBefore := environment.appPort(t, "base", "standalone")

	if err := environment.executor.Stop(t.Context(), environment.base, []string{"standalone"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	environment.start(t, environment.base, StartOptions{}, "standalone")

	if portAfter := environment.appPort(t, "base", "standalone"); portAfter != portBefore {
		t.Errorf("port moved from %d to %d", portBefore, portAfter)
	}
}

func TestAllocationSkipsPortsOfOtherStacksAndBusyPorts(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.IsPortFreeOnHost = func(port int) bool { return port != config.DefaultFirstPort }
	environment.start(t, environment.base, StartOptions{}, "standalone")
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	masterPort := environment.appPort(t, "base", "standalone")
	worktreePort := environment.appPort(t, "proj-1234", "standalone")
	if masterPort == config.DefaultFirstPort || masterPort == worktreePort {
		t.Errorf("base got %d and the worktree got %d", masterPort, worktreePort)
	}
}

func TestStopRemovesTheProcessAndWarnsAboutDependentsInOtherStacks(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if err := environment.executor.Stop(t.Context(), environment.base, []string{"internalapi"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, isRunning := environment.supervisor.runningSpecs["internalapi.base.platform"]; isRunning {
		t.Error("internalapi.base.platform still runs")
	}
	if !strings.Contains(environment.output.String(), "publicapi.proj-1234.platform") {
		t.Errorf("output %q does not warn about publicapi.proj-1234.platform", environment.output.String())
	}
}

func TestTheProxyGetsARouteForEveryRoutedApp(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	route, isRouted := environment.supervisor.routeOf("publicapi.proj-1234.platform.dev.example.net")
	if !isRouted || route.Port != environment.appPort(t, "proj-1234", "publicapi") {
		t.Errorf("route of publicapi = %+v, routed %t; want its http port. Routes: %+v", route, isRouted, environment.supervisor.routes)
	}
	if want := "cd '/worktrees/proj-1234' && ilot start publicapi"; route.StartHint != want {
		t.Errorf("start hint = %q, want %q", route.StartHint, want)
	}
	if _, isRouted := environment.supervisor.routeOf("gateway.base.platform.dev.example.net"); isRouted {
		t.Error("gateway.base.platform has a route, but the app declares none")
	}
}

func TestAStoppedAppKeepsItsRoute(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi", "standalone")

	if err := environment.executor.Stop(t.Context(), environment.featureStack, []string{"publicapi"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, isRouted := environment.supervisor.routeOf("publicapi.proj-1234.platform.dev.example.net"); !isRouted {
		t.Errorf("routes after the stop = %+v, want the route of publicapi kept: its URL then says how to start it", environment.supervisor.routes)
	}
}

func TestListShowsAppsWithLiveStatusAndURL(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Machine.ProxyPort = "18080"
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	listing, err := environment.executor.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	publicapiRow := rowFor(listing.Apps, "proj-1234", "publicapi")
	if publicapiRow.Status != "ready" || publicapiRow.URL != "http://publicapi.proj-1234.platform.dev.example.net:18080" {
		t.Errorf("publicapi row = %+v", publicapiRow)
	}
	if publicapiRow.Ports["http"] != environment.appPort(t, "proj-1234", "publicapi") {
		t.Errorf("publicapi ports = %v, want the http port of its state", publicapiRow.Ports)
	}
	if temporalRow := rowFor(listing.Apps, "base", "temporal"); temporalRow.Status != "ready" || temporalRow.URL != "http://temporal.base.platform.dev.example.net:18080" {
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

func TestAnAppWithTheScopeBaseRunsOnceInTheBaseStackWithItsFixedPorts(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "doc")
	environment.start(t, environment.base, StartOptions{}, "doc")

	if want := []string{"doc.base.platform"}; !slices.Equal(environment.supervisor.startedInOrder, want) {
		t.Errorf("started %v, want %v", environment.supervisor.startedInOrder, want)
	}
	if port := environment.appPort(t, "base", "doc"); port != 10007 {
		t.Errorf("doc port = %d, want its fixed 10007", port)
	}
}

func TestBaseGatewayUsesItsFixedPortAndWorktreeGatewaysDoNot(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if port := environment.appPort(t, "base", "gateway"); port != 9090 {
		t.Errorf("base gateway port = %d, want 9090", port)
	}
	if port := environment.appPort(t, "proj-1234", "gateway"); port < config.DefaultFirstPort || port > config.DefaultLastPort {
		t.Errorf("worktree gateway port = %d, want one from the reserved range", port)
	}
}

func TestStartRefusesAFixedPortHeldByAnotherProcess(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.IsPortFreeOnHost = func(port int) bool { return port != 9090 }

	err := environment.executor.Start(t.Context(), environment.base, []string{"gateway"}, StartOptions{})

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

	if got := environment.supervisor.runningSpecs["restapi.base.platform"].Dir; got != "/legacy/restapi" {
		t.Errorf("restapi WorkingDir = %q", got)
	}
	if got := environment.supervisor.runningSpecs["standalone.proj-1234.platform"].Dir; got != "/worktrees/proj-1234" {
		t.Errorf("standalone WorkingDir = %q", got)
	}
}

func TestWatchedAppRunsWithAWatcherThatRunsIlotRestart(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"PATH": "/usr/bin", "HOME": "/home/jdoe"})
	environment.executor.Machine.IlotEnv = map[string]string{"XDG_STATE_HOME": "/scratch/state"}

	environment.start(t, environment.base, StartOptions{}, "restapi")

	watcher, isRunning := environment.supervisor.runningSpecs["restapi-watch.base.platform"]
	if !isRunning {
		t.Fatalf("no watcher among %v", slices.Sorted(maps.Keys(environment.supervisor.runningSpecs)))
	}
	for _, wantPart := range []string{"watchexec --postpone", "--watch '/legacy/restapi'", "--exts 'pl,pm'", `-- 'cd '"'"'/src/platform'"'"' && '"'"'/opt/ilot/bin/ilot'"'"' restart --stack '"'"'base'"'"' '"'"'restapi'"'"''`} {
		if !strings.Contains(watcher.Command, wantPart) {
			t.Errorf("watcher command %q lacks %q", watcher.Command, wantPart)
		}
	}
	if strings.Contains(watcher.Command, "flock") {
		t.Errorf("watcher command %q uses the flock command, which macOS does not have", watcher.Command)
	}
	if want := map[string]string{"PATH": "/usr/bin", "HOME": "/home/jdoe", "XDG_STATE_HOME": "/scratch/state"}; !maps.Equal(watcher.Env, want) {
		t.Errorf("watcher environment = %v, want %v: the shell, and what makes ilot find the same state", watcher.Env, want)
	}

	if err := environment.executor.Stop(t.Context(), environment.base, []string{"restapi"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, isRunning := environment.supervisor.runningSpecs["restapi-watch.base.platform"]; isRunning {
		t.Error("the watcher outlives its app")
	}
}

func TestTheWatcherOfATicketStackNamesItsStack(t *testing.T) {
	environment := newTestEnvironment(t)
	portalStandalone := environment.executor.Config.Apps["portal-standalone"]
	portalStandalone.Watch = config.Watch{Paths: []string{"."}, Exts: []string{"go"}}
	environment.executor.Config.Apps["portal-standalone"] = portalStandalone

	environment.start(t, environment.featureStack, StartOptions{}, "portal-standalone")

	watcher := environment.supervisor.runningSpecs["portal-standalone-watch.proj-1234.platform"]
	if !strings.Contains(watcher.Command, "--watch '/src/portal'") || !strings.Contains(watcher.Command, `restart --stack '"'"'proj-1234'"'"' '"'"'portal-standalone'"'"'`) {
		t.Errorf("watcher command %q, want the main checkout of portal watched for the stack proj-1234", watcher.Command)
	}
}

func TestRestartAppsBuildsThenRestartsTheNamedApp(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "internalapi")
	*environment.builds = nil
	environment.supervisor.events = nil

	if err := environment.executor.RestartApps(t.Context(), environment.featureStack, []string{"internalapi"}); err != nil {
		t.Fatalf("RestartApps: %v", err)
	}

	if len(*environment.builds) != 1 || (*environment.builds)[0].workingDir != "/worktrees/proj-1234" {
		t.Errorf("builds = %+v, want one build in the worktree", *environment.builds)
	}
	if want := []string{"restart internalapi.proj-1234.platform", "ready internalapi.proj-1234.platform"}; !slices.Equal(environment.supervisor.events, want) {
		t.Errorf("events = %v, want %v: the dependencies stay as they are", environment.supervisor.events, want)
	}
}

func TestRestartAppsKeepsTheRunningAppWhenItsBuildFails(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "internalapi")
	environment.supervisor.events = nil
	environment.executor.RunBuild = func(context.Context, string, string, []string) error { return errors.New("exit status 1") }

	err := environment.executor.RestartApps(t.Context(), environment.featureStack, []string{"internalapi"})

	if err == nil || !strings.Contains(err.Error(), "build internalapi.proj-1234.platform") {
		t.Fatalf("RestartApps = %v, want the failed build", err)
	}
	if len(environment.supervisor.events) != 0 || !environment.isApplied("internalapi.proj-1234.platform") {
		t.Errorf("events = %v, want none: the old version keeps running", environment.supervisor.events)
	}
}

func TestRestartAppsRefusesAnAppThatDoesNotRun(t *testing.T) {
	environment := newTestEnvironment(t)

	err := environment.executor.RestartApps(t.Context(), environment.featureStack, []string{"internalapi"})

	if err == nil || !strings.Contains(err.Error(), "internalapi.proj-1234.platform does not run") || !strings.Contains(err.Error(), "ilot start internalapi") {
		t.Fatalf("RestartApps = %v, want a refusal that names ilot start", err)
	}
	if len(*environment.builds) != 0 || environment.supervisor.daemonIsUp {
		t.Errorf("builds = %+v, daemon up %t; want nothing done: a watcher must not start again an app that the user stopped", *environment.builds, environment.supervisor.daemonIsUp)
	}
}

func TestRestartAppsStartsOneTimeAProcessThatTheDaemonLost(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	environment.supervisor.simulateDaemonRestart()

	if err := environment.executorOfANewCommand().RestartApps(t.Context(), environment.featureStack, []string{"standalone"}); err != nil {
		t.Fatalf("RestartApps: %v", err)
	}

	if want := []string{"start standalone.proj-1234.platform", "ready standalone.proj-1234.platform"}; !slices.Equal(environment.supervisor.events, want) {
		t.Errorf("events = %v, want %v", environment.supervisor.events, want)
	}
}

func TestAnAppWithTheScopeBaseHasItsRouteInTheBaseStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "temporal")

	if route, isRouted := environment.supervisor.routeOf("temporal.base.platform.dev.example.net"); !isRouted || route.Port != 10012 {
		t.Errorf("route of temporal = %+v, routed %t; want the temporal UI on port 10012", route, isRouted)
	}
}

func TestBootStartsAutostartAppsAndTheAppsThatWereRunning(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	environment.supervisor.simulateDaemonRestart()

	if err := environment.executor.Boot(t.Context(), environment.base); err != nil {
		t.Fatalf("Boot: %v", err)
	}

	running := slices.Sorted(maps.Keys(environment.supervisor.runningSpecs))
	want := []string{"gateway.base.platform", "internalapi.base.platform", "restapi-watch.base.platform", "restapi.base.platform", "standalone.proj-1234.platform", "temporal.base.platform"}
	if !slices.Equal(running, want) {
		t.Errorf("running after boot %v, want %v", running, want)
	}
	if temporalIndex, apiIndex := slices.Index(environment.supervisor.startedInOrder, "temporal.base.platform"), slices.Index(environment.supervisor.startedInOrder, "internalapi.base.platform"); temporalIndex > apiIndex {
		t.Errorf("started %v: want temporal before internalapi", environment.supervisor.startedInOrder)
	}
}

func TestBootDoesNothingWhenTheDaemonRuns(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "standalone")
	environment.supervisor.startedInOrder = nil

	if err := environment.executor.Boot(t.Context(), environment.base); err != nil {
		t.Fatalf("Boot: %v", err)
	}

	if len(environment.supervisor.startedInOrder) != 0 {
		t.Errorf("Boot started %v although the daemon already ran", environment.supervisor.startedInOrder)
	}
}

func TestBootBuildsOnlyStacksWithoutBinaries(t *testing.T) {
	environment := newTestEnvironment(t)
	masterBinDir := filepath.Join(environment.executor.StackDir("base"), "bin")
	if err := os.MkdirAll(masterBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(masterBinDir, "platform"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := environment.executor.Boot(t.Context(), environment.base); err != nil {
		t.Fatalf("Boot: %v", err)
	}

	if len(*environment.builds) != 0 {
		t.Errorf("Boot rebuilt %v although base has its binary", *environment.builds)
	}
}

func TestBootKeepsAFailedAppAMemberAndStartsTheOthers(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "publicapi")
	environment.supervisor.simulateDaemonRestart()
	environment.supervisor.setBehavior("internalapi.base.platform", processEndsAtEveryStart)

	err := environment.executor.Boot(t.Context(), environment.base)

	if err == nil || !strings.Contains(err.Error(), "internalapi.base.platform") {
		t.Fatalf("Boot = %v, want an error naming internalapi.base.platform", err)
	}
	if !strings.Contains(err.Error(), "publicapi.base.platform") {
		t.Errorf("Boot = %v, want it to name publicapi.base.platform as skipped", err)
	}
	started := environment.supervisor.startedInOrder
	for _, wantStarted := range []string{"temporal.base.platform", "restapi.base.platform", "gateway.base.platform"} {
		if !slices.Contains(started, wantStarted) {
			t.Errorf("started %v, want %s despite the failure", started, wantStarted)
		}
	}
	if slices.Contains(started, "publicapi.base.platform") {
		t.Errorf("started %v, want publicapi.base.platform skipped because internalapi.base.platform failed", started)
	}
	if !environment.isStoredAsRunning(t, "base", "internalapi") || !environment.isStoredAsRunning(t, "base", "publicapi") {
		t.Error("a failed or skipped app lost its membership at boot: the next ilot start would not start it again")
	}
	if !environment.isStoredAsRunning(t, "base", "temporal") || !environment.isApplied("temporal.base.platform") {
		t.Error("temporal.base.platform started well at boot but is not kept running")
	}
}

func TestBootSkipsAnAppWhoseDependencyFailedInTheTicketStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "internalapi", "publicapi")
	environment.supervisor.simulateDaemonRestart()
	environment.supervisor.setBehavior("internalapi.proj-1234.platform", processEndsAtEveryStart)

	err := environment.executor.Boot(t.Context(), environment.base)

	if err == nil || !strings.Contains(err.Error(), "publicapi.proj-1234.platform skipped: it depends on internalapi.proj-1234.platform") {
		t.Fatalf("Boot = %v, want publicapi.proj-1234.platform skipped because internalapi.proj-1234.platform failed", err)
	}
	if slices.Contains(environment.supervisor.startedInOrder, "publicapi.proj-1234.platform") {
		t.Errorf("started %v, want publicapi.proj-1234.platform skipped", environment.supervisor.startedInOrder)
	}
}

func TestBootSkipsAContainerWhoseDependencyImageFailed(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")
	environment.supervisor.simulateDaemonRestart()
	environment.executor.EnsureImage = (&imageRecorder{failing: map[string]string{"registry/search-cache:latest": "unauthorized"}}).ensure

	err := environment.executor.Boot(t.Context(), environment.base)

	if err == nil || !strings.Contains(err.Error(), "search.proj-1234.platform skipped: it depends on search-cache.proj-1234.platform") {
		t.Fatalf("Boot = %v, want search.proj-1234.platform skipped because the image of search-cache.proj-1234.platform failed", err)
	}
	if slices.Contains(environment.supervisor.startedInOrder, "search.proj-1234.platform") {
		t.Errorf("started %v, want search.proj-1234.platform skipped", environment.supervisor.startedInOrder)
	}
}

func TestHomeURLIsTheRouteListOfTheProxy(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Machine.ProxyPort = "18080"

	if got := environment.executor.Machine.HomeURL(); got != "http://dev.example.net:18080" {
		t.Errorf("HomeURL = %q", got)
	}
}

func TestTheRouteOfAnAppCarriesItsHomePath(t *testing.T) {
	environment := newTestEnvironment(t)
	publicapi := environment.executor.Config.Apps["publicapi"]
	publicapi.HomePath = "/swagger/"
	environment.executor.Config.Apps["publicapi"] = publicapi

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if route, _ := environment.supervisor.routeOf("publicapi.proj-1234.platform.dev.example.net"); route.HomePath != "/swagger/" {
		t.Errorf("route of publicapi = %+v, want the home path /swagger/", route)
	}
}

func TestListShowsTheHomePathInTheURL(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Machine.ProxyPort = "18080"
	publicapi := environment.executor.Config.Apps["publicapi"]
	publicapi.HomePath = "/swagger/"
	environment.executor.Config.Apps["publicapi"] = publicapi
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	listing, err := environment.executor.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if url := rowFor(listing.Apps, "proj-1234", "publicapi").URL; url != "http://publicapi.proj-1234.platform.dev.example.net:18080/swagger/" {
		t.Errorf("publicapi URL = %q", url)
	}
}

func TestLogFileOfAnAppIsInTheStackThatRunsIt(t *testing.T) {
	environment := newTestEnvironment(t)

	worktreeLog, err := environment.executor.LogFile(environment.featureStack, "publicapi")
	if err != nil {
		t.Fatalf("LogFile(publicapi): %v", err)
	}
	if want := filepath.Join(environment.executor.StackDir("proj-1234"), "logs", "publicapi.log"); worktreeLog != want {
		t.Errorf("publicapi log = %q, want %q", worktreeLog, want)
	}
	baseLog, err := environment.executor.LogFile(environment.featureStack, "temporal")
	if err != nil {
		t.Fatalf("LogFile(temporal): %v", err)
	}
	if want := filepath.Join(environment.executor.StackDir("base"), "logs", "temporal.log"); baseLog != want {
		t.Errorf("temporal log = %q, want %q", baseLog, want)
	}
	if _, err := environment.executor.LogFile(environment.featureStack, "nope"); err == nil {
		t.Error("want an error for an unknown app")
	}
}

func TestAppRunsFromTheCheckoutOfItsRepo(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "portal-standalone", "standalone")

	portalSpec := environment.supervisor.runningSpecs["portal-standalone.proj-1234.platform"]
	if portalSpec.Dir != "/src/portal" || !strings.HasSuffix(portalSpec.Command, "--from=/src/portal") {
		t.Errorf("portal-standalone spec = %+v, want portal's main checkout", portalSpec)
	}
	if got := environment.supervisor.runningSpecs["standalone.proj-1234.platform"].Dir; got != "/worktrees/proj-1234" {
		t.Errorf("standalone WorkingDir = %q, want platform's worktree", got)
	}
	if !slices.Contains(*environment.builds, recordedBuild{workingDir: "/src/portal", command: "go build ./cmd/api"}) {
		t.Errorf("builds = %+v, want the portal build in portal's checkout", *environment.builds)
	}
}

func environmentOf(t *testing.T, environment testEnvironment, appName string) map[string]plan.EnvEntry {
	t.Helper()
	entries, err := environment.executor.Environment(environment.featureStack, appName)
	if err != nil {
		t.Fatalf("Environment(%s): %v", appName, err)
	}
	entryByKey := map[string]plan.EnvEntry{}
	for _, entry := range entries {
		entryByKey[entry.Key] = entry
	}
	return entryByKey
}

func TestEnvironmentAppliesTheSourcesInOrder(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Machine.Login = "jdoe"
	environment.executor.Config.RepoEnv = map[string]map[string]string{"platform": {"LOG_LEVEL": "info", "REGION": "DEV1"}}
	standalone := environment.executor.Config.Apps["standalone"]
	standalone.Env = map[string]string{"PORT": "{{.Ports.http}}", "REGION": "EU1"}
	environment.executor.Config.Apps["standalone"] = standalone
	environment.executor.Settings = config.WorkspaceSettings{
		Repos: nil,
		Env:   map[string]string{"LOG_LEVEL": "debug", "PORT": "1"},
		Apps:  map[string]map[string]string{"standalone": {"API_TOKEN": "custom"}},
	}
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	entryByKey := environmentOf(t, environment, "standalone")

	wantPort := strconv.Itoa(environment.appPort(t, "proj-1234", "standalone"))
	want := map[string]plan.EnvEntry{
		"LOG_LEVEL": {Key: "LOG_LEVEL", Value: "debug", Source: "global"},
		"REGION":    {Key: "REGION", Value: "EU1", Source: "app"},
		"PORT":      {Key: "PORT", Value: wantPort, Source: "computed"},
		"API_TOKEN": {Key: "API_TOKEN", Value: "custom", Source: "global app"},
	}
	for key, wantEntry := range want {
		if entryByKey[key] != wantEntry {
			t.Errorf("%s = %+v, want %+v", key, entryByKey[key], wantEntry)
		}
	}
	if environment.supervisor.runningSpecs["standalone.proj-1234.platform"].Env["LOG_LEVEL"] != "debug" {
		t.Errorf("the process does not get the environment: %v", environment.supervisor.runningSpecs["standalone.proj-1234.platform"].Env)
	}
}

func TestTemplatesKnowTheStackTheUserRepoAndURL(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Machine.Login = "jdoe"
	environment.executor.Machine.ProxyPort = "18080"
	standalone := environment.executor.Config.Apps["standalone"]
	standalone.Run = `run {{.Stack}} {{.User}} {{repo "portal"}} {{url "temporal"}} {{url "internalapi"}}`
	environment.executor.Config.Apps["standalone"] = standalone

	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	want := "run proj-1234 jdoe /src/portal http://temporal.base.platform.dev.example.net:18080 http://internalapi.base.platform.dev.example.net:18080"
	if got := environment.supervisor.runningSpecs["standalone.proj-1234.platform"].Command; got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

func TestContainerCommandRemovesALeftoverContainerFirst(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")

	command := environment.supervisor.runningSpecs["search.proj-1234.platform"].Command
	if !strings.HasPrefix(command, "docker rm -f 'ilot_search_proj-1234_platform' >/dev/null 2>&1 || true && exec docker run --rm --name 'ilot_search_proj-1234_platform' ") {
		t.Errorf("command = %q", command)
	}
}

func TestAContainerBuildRunsBeforeItsContainerStarts(t *testing.T) {
	environment := newTestEnvironment(t)
	search := environment.executor.Config.Apps["search"]
	search.Build = "prepare-files"
	environment.executor.Config.Apps["search"] = search

	environment.start(t, environment.featureStack, StartOptions{}, "search")

	var ranPrepare bool
	for _, build := range *environment.builds {
		ranPrepare = ranPrepare || strings.Contains(build.command, "prepare-files")
	}
	if !ranPrepare {
		t.Errorf("builds %v lack the container build", *environment.builds)
	}
}

func TestContainerCommandPublishesItsPortsOnTheLoopbackAddress(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")

	command := environment.supervisor.runningSpecs["search.proj-1234.platform"].Command
	searchPort := environment.appPort(t, "proj-1234", "search")
	for _, wantPart := range []string{
		"--add-host 'host.docker.internal:host-gateway'",
		"-p 127.0.0.1:" + strconv.Itoa(searchPort) + ":16271",
		"--env-file '/etc/acme.conf.d/search.conf'",
		"-e 'REGION'",
		"-e 'GATEWAY_URL'",
		"-v '/src/portal/schemas:/src/schemas'",
		"'--shm-size=1g' '--label=stack=proj-1234' 'registry/search:latest'",
	} {
		if !strings.Contains(command, wantPart) {
			t.Errorf("command %q lacks %q", command, wantPart)
		}
	}
	if !strings.HasSuffix(command, "'registry/search:latest'") {
		t.Errorf("command %q does not end with the image", command)
	}
	for _, removedPart := range []string{"--network", "--link"} {
		if strings.Contains(command, removedPart) {
			t.Errorf("command %q still has %s: the loopback ports replace the container network", command, removedPart)
		}
	}
	if strings.Contains(command, "DEV1") {
		t.Errorf("command %q shows an env value, which ps would show to every user", command)
	}
	if searchPort < config.DefaultFirstPort || searchPort > config.DefaultLastPort {
		t.Errorf("search is published on port %d, want a port of the range of ilot", searchPort)
	}
	if order := environment.supervisor.startedInOrder; slices.Index(order, "search-cache.proj-1234.platform") > slices.Index(order, "search.proj-1234.platform") {
		t.Errorf("started %v: want the cache before search", order)
	}
}

func TestAContainerReachesTheOtherAppsThroughTheNameOfTheMachine(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")

	searchEnv := environment.supervisor.runningSpecs["search.proj-1234.platform"].Env
	wantGateway := "http://host.docker.internal:" + strconv.Itoa(environment.appPort(t, "proj-1234", "gateway"))
	wantCache := "postgres://host.docker.internal:" + strconv.Itoa(environment.storedApp(t, "proj-1234", "search-cache").Ports["postgres"]) + "/cache"
	if searchEnv["REGION"] != "DEV1" || searchEnv["GATEWAY_URL"] != wantGateway || searchEnv["CACHE_URL"] != wantCache {
		t.Errorf("search env = %v, want %s for a process and %s for another container", searchEnv, wantGateway, wantCache)
	}
}

func TestAContainerIsProbedAndRoutedOnItsLoopbackPort(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")

	searchPort := environment.appPort(t, "proj-1234", "search")
	if probe := environment.supervisor.runningSpecs["search.proj-1234.platform"].Ready; probe == nil || probe.Host != "" || probe.Port != searchPort {
		t.Errorf("search probe = %+v, want the published port %d on the loopback address", probe, searchPort)
	}
	if probe := environment.supervisor.runningSpecs["search-cache.proj-1234.platform"].Ready; probe == nil || probe.Command != "docker exec 'ilot_search-cache_proj-1234_platform' sh -c 'pg_isready -U postgres'" {
		t.Errorf("cache probe = %+v", probe)
	}
	if route, isRouted := environment.supervisor.routeOf("search.proj-1234.platform.dev.example.net"); !isRouted || route.Port != searchPort {
		t.Errorf("route of search = %+v, routed %t; want the published port %d", route, isRouted, searchPort)
	}
}

func TestAProcessReachesAContainerOnLocalhost(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "front-like")

	want := "front --api=http://localhost:" + strconv.Itoa(environment.appPort(t, "proj-1234", "search")) + " --gw=localhost"
	if got := environment.supervisor.runningSpecs["front-like.proj-1234.platform"].Command; got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

func TestTwoStacksPublishTheSameContainerOnTwoPorts(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")
	environment.start(t, environment.base, StartOptions{}, "search")

	ticketPort, masterPort := environment.appPort(t, "proj-1234", "search"), environment.appPort(t, "base", "search")
	if ticketPort == masterPort {
		t.Errorf("both stacks publish search on port %d: the second container could not start", ticketPort)
	}
}

func TestAContainerPortThatAnotherProgramHoldsIsNotGiven(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.IsPortFreeOnHost = func(port int) bool { return port != config.DefaultFirstPort }

	environment.start(t, environment.featureStack, StartOptions{SkipDependencies: true}, "search-cache")

	if port := environment.storedApp(t, "proj-1234", "search-cache").Ports["postgres"]; port == config.DefaultFirstPort || port == 0 {
		t.Errorf("search-cache is published on port %d, want a free port", port)
	}
}

func TestADependencyWithoutAScopeComesFromBase(t *testing.T) {
	environment := newTestEnvironment(t)
	search := environment.executor.Config.Apps["search"]
	search.Scope = ""
	environment.executor.Config.Apps["search"] = search

	environment.start(t, environment.featureStack, StartOptions{}, "front-like")

	want := "front --api=http://localhost:" + strconv.Itoa(environment.appPort(t, "base", "search")) + " --gw=localhost"
	if environment.supervisor.runningSpecs["front-like.proj-1234.platform"].Command != want {
		t.Errorf("Command = %q, want %q", environment.supervisor.runningSpecs["front-like.proj-1234.platform"].Command, want)
	}
}

func TestDomainTemplateGivesTheProxyDomain(t *testing.T) {
	environment := newTestEnvironment(t)
	standalone := environment.executor.Config.Apps["standalone"]
	standalone.Env = map[string]string{"ALLOWED_HOSTS": ".{{.Domain}}"}
	environment.executor.Config.Apps["standalone"] = standalone

	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	if got := environment.supervisor.runningSpecs["standalone.proj-1234.platform"].Env["ALLOWED_HOSTS"]; got != ".dev.example.net" {
		t.Errorf("ALLOWED_HOSTS = %q, want .dev.example.net", got)
	}
}

func TestProxyPortTemplateGivesTheProxyPort(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Machine.ProxyPort = "18080"
	standalone := environment.executor.Config.Apps["standalone"]
	standalone.Env = map[string]string{"EXPOSED_PORT": "{{.ProxyPort}}"}
	environment.executor.Config.Apps["standalone"] = standalone

	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	if got := environment.supervisor.runningSpecs["standalone.proj-1234.platform"].Env["EXPOSED_PORT"]; got != "18080" {
		t.Errorf("EXPOSED_PORT = %q, want 18080", got)
	}
}

func TestAnAppWithAStopCommandStopsWithItRendered(t *testing.T) {
	environment := newTestEnvironment(t)
	standalone := environment.executor.Config.Apps["standalone"]
	standalone.Run = "docker run --rm --name standalone-{{.Stack}} -p {{.Ports.http}}:80 nginx"
	standalone.Stop = "docker stop standalone-{{.Stack}}"
	environment.executor.Config.Apps["standalone"] = standalone

	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	if got, want := environment.supervisor.runningSpecs["standalone.proj-1234.platform"].Stop, "docker stop standalone-proj-1234"; got != want {
		t.Errorf("Stop = %q, want %q", got, want)
	}
}

func TestAContainerStopsWithDockerStopAndAProcessWithItsSignal(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "search")

	if got := environment.supervisor.runningSpecs["search-cache.proj-1234.platform"].Stop; got != "docker stop -t 10 'ilot_search-cache_proj-1234_platform'" {
		t.Errorf("search-cache Stop = %q", got)
	}
	if got := environment.supervisor.runningSpecs["gateway.proj-1234.platform"].Stop; got != "" {
		t.Errorf("gateway Stop = %q, want none", got)
	}
}

func TestTheEnvOfTheRootRepoReachesTheAppsOfEveryRepoBelowTheEnvOfTheirOwnRepo(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Config.RepoEnv = map[string]map[string]string{
		"platform": {"LOG_LEVEL": "info", "REGION": "DEV1", "STACK_NAME": "{{.Stack}}"},
		"portal":   {"REGION": "EU2"},
	}
	environment.start(t, environment.featureStack, StartOptions{}, "portal-standalone")

	entryByKey := environmentOf(t, environment, "portal-standalone")

	want := map[string]plan.EnvEntry{
		"LOG_LEVEL":  {Key: "LOG_LEVEL", Value: "info", Source: "root repo"},
		"REGION":     {Key: "REGION", Value: "EU2", Source: "repo"},
		"STACK_NAME": {Key: "STACK_NAME", Value: "proj-1234", Source: "computed"},
	}
	for key, wantEntry := range want {
		if entryByKey[key] != wantEntry {
			t.Errorf("%s = %+v, want %+v", key, entryByKey[key], wantEntry)
		}
	}
}

func TestAMoreSpecificLiteralBeatsATemplatedValueOfTheRootRepo(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Config.RepoEnv = map[string]map[string]string{
		"platform": {"REGION": "{{.Stack}}", "DB_URL": "db-{{.Stack}}", "TOKEN": "{{.Stack}}"},
		"portal":   {"DB_URL": "db-of-portal"},
	}
	portalStandalone := environment.executor.Config.Apps["portal-standalone"]
	portalStandalone.Env = map[string]string{"REGION": "EU1"}
	environment.executor.Config.Apps["portal-standalone"] = portalStandalone
	environment.executor.Settings = config.WorkspaceSettings{Repos: nil, Env: map[string]string{"DB_URL": "db-of-the-user", "TOKEN": "of-the-user"}, Apps: nil}
	environment.start(t, environment.featureStack, StartOptions{}, "portal-standalone")

	entryByKey := environmentOf(t, environment, "portal-standalone")

	want := map[string]plan.EnvEntry{
		"REGION": {Key: "REGION", Value: "EU1", Source: "app"},
		"DB_URL": {Key: "DB_URL", Value: "db-of-the-user", Source: "global"},
		"TOKEN":  {Key: "TOKEN", Value: "proj-1234", Source: "computed"},
	}
	for key, wantEntry := range want {
		if entryByKey[key] != wantEntry {
			t.Errorf("%s = %+v, want %+v", key, entryByKey[key], wantEntry)
		}
	}
}

func TestAnUnknownAppNamesTheReposThatAreNotCloned(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ReposNotCloned = map[string]string{"portal": "/home/user/go/src/portal"}

	err := environment.executor.Start(t.Context(), environment.featureStack, []string{"portal-front"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "workspaces.platform.repos.portal in ~/.config/ilot/config.yaml") || !strings.Contains(err.Error(), "/home/user/go/src/portal") {
		t.Fatalf("Start = %v, want an error naming the repo that is not cloned", err)
	}
}

func TestPersonalEnvReachesAContainerOnlyThroughItsAppSettings(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Settings = config.WorkspaceSettings{
		Repos: nil,
		Env:   map[string]string{"DEBUG_TOKEN": "secret"},
		Apps:  map[string]map[string]string{"search": {"FEATURE_FLAG": "true"}},
	}

	environment.start(t, environment.featureStack, StartOptions{}, "search")

	searchSpec := environment.supervisor.runningSpecs["search.proj-1234.platform"]
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
	environment.executor.Config.RepoEnv = map[string]map[string]string{"platform": {"LOGKIT_LOGJSON": "1"}}
	environment.start(t, environment.base, StartOptions{}, "standalone")
	if got := environment.supervisor.runningSpecs["standalone.base.platform"].Env["LOGKIT_LOGJSON"]; got != "1" {
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
	environment.executor.EnsureImage = recorder.ensure

	err := environment.executor.Start(t.Context(), environment.featureStack, []string{"search", "publicapi"}, StartOptions{})

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
	search := environment.executor.Config.Apps["search"]
	search.Container.Image = "registry/search-cache:latest"
	environment.executor.Config.Apps["search"] = search
	recorder := &imageRecorder{failing: nil}
	environment.executor.EnsureImage = recorder.ensure

	environment.start(t, environment.featureStack, StartOptions{}, "search")

	if want := []string{"registry/search-cache:latest"}; !slices.Equal(recorder.checked, want) {
		t.Errorf("images checked %v, want %v", recorder.checked, want)
	}
}

func TestStartChecksNoImageForARunningContainer(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")
	recorder := &imageRecorder{failing: nil}
	environment.executor.EnsureImage = recorder.ensure

	environment.start(t, environment.featureStack, StartOptions{}, "search")

	if len(recorder.checked) != 0 {
		t.Errorf("images checked %v, want none for containers that already run", recorder.checked)
	}
}

func TestBootSkipsTheAppWhoseImageFailsAndStartsTheOthers(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "front-like")
	environment.supervisor.simulateDaemonRestart()
	environment.executor.EnsureImage = (&imageRecorder{failing: map[string]string{"registry/search:latest": "unauthorized"}}).ensure

	err := environment.executor.Boot(t.Context(), environment.base)

	if err == nil || !strings.Contains(err.Error(), "search.base.platform: pull registry/search:latest") || !strings.Contains(err.Error(), "front-like.base.platform skipped") {
		t.Fatalf("Boot = %v, want the failed image named with its app and stack, and front-like.base.platform skipped", err)
	}
	started := environment.supervisor.startedInOrder
	if !slices.Contains(started, "temporal.base.platform") || !slices.Contains(started, "gateway.base.platform") {
		t.Errorf("started %v, want the other apps started", started)
	}
	if slices.Contains(started, "search.base.platform") || slices.Contains(started, "front-like.base.platform") {
		t.Errorf("started %v, want search and front-like skipped", started)
	}
}

func TestStartWithNoContainerChecksNoImage(t *testing.T) {
	environment := newTestEnvironment(t)
	recorder := &imageRecorder{failing: nil}
	environment.executor.EnsureImage = recorder.ensure

	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	if len(recorder.checked) != 0 {
		t.Errorf("images checked %v, want none without a container", recorder.checked)
	}
}

func fixedShellEnv(valueByKey map[string]string) func() (map[string]string, map[string]string, error) {
	return func() (map[string]string, map[string]string, error) { return valueByKey, map[string]string{}, nil }
}
