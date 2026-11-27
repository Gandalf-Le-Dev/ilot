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
		if _, isHeld := supervisor.runningSpecs[processName]; !isHeld {
			return fmt.Errorf("%s: %w", processName, daemon.ErrUnknownProcess)
		}
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
	const buildShop = "go build -o {{.BinDir}}/shop ./cmd/shop"
	const routerAddress = `http://localhost:{{port "router" "http"}}`
	return config.Config{
		External: map[string]config.ExternalService{
			"secrets":   {Port: 8200, StartHint: "restart base"},
			"legacyapi": {Port: 10004, StartHint: "start it by hand"},
		},
		Apps: map[string]config.App{
			"scheduler": {
				Scope:      "base",
				Run:        "scheduler server start-dev --port {{.Ports.grpc}} --ui-port {{.Ports.ui}}",
				Ports:      []string{"grpc", "ui"},
				FixedPorts: map[string]int{"grpc": 7233, "ui": 10012},
				Route:      "ui",
				Ready:      config.ReadyCheck{Port: "grpc", Path: ""},
			},
			"restapi": {
				Scope:      "base",
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
			"router": {
				Scope:      "stack",
				Run:        "routerd -listen-addr :{{.Ports.http}}",
				Ports:      []string{"http"},
				FixedPorts: map[string]int{"http": 9090},
			},
			"internalapi": {
				Build:     buildShop,
				Run:       "{{.BinDir}}/shop api internalapi --port={{.Ports.http}}",
				Env:       map[string]string{"ROUTER_URL": routerAddress, "SCHEDULER_ADDRESS": `localhost:{{port "scheduler" "grpc"}}`},
				Ports:     []string{"http"},
				Route:     "http",
				DependsOn: []string{"router", "scheduler"},
			},
			"publicapi": {
				Build:     buildShop,
				Run:       "{{.BinDir}}/shop api publicapi --port={{.Ports.http}}",
				Env:       map[string]string{"ROUTER_URL": routerAddress},
				Ports:     []string{"http"},
				Route:     "http",
				DependsOn: []string{"router", "internalapi", "scheduler", "secrets"},
			},
			"standalone": {
				Run:   "standalone --port={{.Ports.http}}",
				Ports: []string{"http"},
			},
			"search": {
				Scope: "stack",
				Route: "http",
				Env: map[string]string{
					"ROUTER_URL": `http://{{host "router"}}:{{port "router" "http"}}`,
					"CACHE_URL":  `postgres://{{host "search-cache"}}:{{port "search-cache"}}/cache`,
					"REGION":     "DEV1",
				},
				DependsOn: []string{"search-cache", "router"},
				Container: config.Container{
					Image:    "registry/search:latest",
					Ports:    map[string]int{"http": 16271},
					Mounts:   map[string]string{`{{repo "studio"}}/schemas`: "/src/schemas"},
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
				Run:       "front --api=http://{{host \"search\"}}:{{port \"search\" \"http\"}} --gw={{host \"router\"}}",
				Ports:     []string{"http"},
				DependsOn: []string{"search"},
			},
			"studio-standalone": {
				Repo:  "studio",
				Build: "go build ./cmd/api",
				Run:   "api --port={{.Ports.http}} --from={{.WorktreeDir}}",
				Ports: []string{"http"},
			},
		},
	}
}

func fakeCheckoutsForStack(stackName string) (map[string]workspace.Worktree, error) {
	checkoutByRepo := map[string]workspace.Worktree{
		"shop":   {Repo: "shop", Dir: "/src/shop", Branch: "base", IsMain: true},
		"studio": {Repo: "studio", Dir: "/src/studio", Branch: "base", IsMain: true},
	}
	if stackName == "task-1234" {
		checkoutByRepo["shop"] = workspace.Worktree{Repo: "shop", Dir: "/worktrees/task-1234", Branch: "TASK-1234-x", IsMain: false}
	} else if stackName != plan.BaseStack {
		checkoutByRepo["shop"] = workspace.Worktree{Repo: "shop", Dir: "/worktrees/" + stackName, Branch: stackName, IsMain: false}
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
			Workspace:         "shop",
			Root:              "/src/shop",
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
		featureStack:  "task-1234",
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

	wantOrder := []string{"router.task-1234.shop", "router.base.shop", "scheduler.base.shop", "internalapi.base.shop", "publicapi.task-1234.shop"}
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
	if want := []string{"/src/shop", "/worktrees/task-1234"}; !slices.Equal(buildDirs, want) {
		t.Errorf("builds ran in %v, want %v", buildDirs, want)
	}
}

func TestEnvironmentTemplatesUseTheRouterOfTheStackAndThePortsOfTheBaseStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	worktreePublicapi := environment.supervisor.runningSpecs["publicapi.task-1234.shop"]
	if want := "http://localhost:" + strconv.Itoa(environment.appPort(t, "task-1234", "router")); worktreePublicapi.Env["ROUTER_URL"] != want {
		t.Errorf("ROUTER_URL = %q, want %q", worktreePublicapi.Env["ROUTER_URL"], want)
	}
	if got := environment.supervisor.runningSpecs["internalapi.base.shop"].Env["SCHEDULER_ADDRESS"]; got != "localhost:7233" {
		t.Errorf("SCHEDULER_ADDRESS = %q", got)
	}
	if worktreePublicapi.Dir != "/worktrees/task-1234" {
		t.Errorf("WorkingDir = %q", worktreePublicapi.Dir)
	}
	wantBinDir := filepath.Join(environment.executor.StackDir("task-1234"), "bin")
	if !strings.HasPrefix(worktreePublicapi.Command, wantBinDir+"/shop api publicapi --port=") {
		t.Errorf("Command = %q", worktreePublicapi.Command)
	}
}

func TestStartRefusesWhenAnExternalDependencyIsDown(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.IsExternalServiceUp = func(int) bool { return false }

	err := environment.executor.Start(t.Context(), environment.featureStack, []string{"publicapi"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "restart base") {
		t.Fatalf("err = %v, want the start hint of secrets", err)
	}
	if len(*environment.builds) != 0 || len(environment.supervisor.startedInOrder) != 0 {
		t.Errorf("built %v and started %v before refusing", *environment.builds, environment.supervisor.startedInOrder)
	}
}

func TestStartWithoutDependenciesStartsOnlyTheRequestedApp(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "router")

	environment.start(t, environment.base, StartOptions{SkipDependencies: true}, "internalapi")

	if want := []string{"router.base.shop", "internalapi.base.shop"}; !slices.Equal(environment.supervisor.startedInOrder, want) {
		t.Errorf("started %v, want %v", environment.supervisor.startedInOrder, want)
	}
}

func TestStartWithoutDependenciesFailsWhenATemplateNeedsAMissingPort(t *testing.T) {
	environment := newTestEnvironment(t)

	err := environment.executor.Start(t.Context(), environment.featureStack, []string{"internalapi"}, StartOptions{SkipDependencies: true})

	if err == nil || !strings.Contains(err.Error(), `"router"`) {
		t.Fatalf("err = %v, want an error naming the router", err)
	}
}

func TestASecondStartOfARunningAppSendsNoCall(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "standalone")
	*environment.builds = nil
	environment.supervisor.calls = nil

	environment.start(t, environment.base, StartOptions{}, "standalone")

	if len(environment.supervisor.calls) != 0 || len(environment.supervisor.restarted) != 0 || len(*environment.builds) != 0 {
		t.Errorf("calls = %v, restarted = %v, builds = %v; want nothing: a start converges, and the app runs", environment.supervisor.calls, environment.supervisor.restarted, *environment.builds)
	}
	if !strings.Contains(environment.output.String(), "standalone.base.shop already runs: ilot restart standalone builds it again") {
		t.Errorf("output = %q, want the line that names ilot restart", environment.output.String())
	}
}

func TestAStartReplacesANamedAppWhoseSpecificationChanged(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "standalone")
	*environment.builds = nil
	environment.supervisor.startedInOrder = nil
	changed := environment.executor.Config.Apps["standalone"]
	changed.Run += " --verbose"
	environment.executor.Config.Apps["standalone"] = changed

	environment.start(t, environment.base, StartOptions{}, "standalone")

	if !slices.Contains(environment.supervisor.startedInOrder, "standalone.base.shop") || len(environment.supervisor.restarted) != 0 {
		t.Errorf("started %v, restarted %v; want standalone.base.shop replaced with its new specification", environment.supervisor.startedInOrder, environment.supervisor.restarted)
	}
}

func TestStartShowsTheLogOfAnAppWhileItWaitsForIt(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.logBeforeReady = map[string]logLineBeforeReady{
		"standalone.base.shop": {logPath: filepath.Join(environment.executor.StackDir("base"), "logs", "standalone.log"), message: "listening"},
	}

	environment.start(t, environment.base, StartOptions{}, "standalone")

	if !strings.Contains(environment.output.String(), "standalone.base.shop | listening\n") {
		t.Errorf("output = %q, want the log line of standalone.base.shop", environment.output.String())
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

	if !strings.Contains(environment.output.String(), "still building publicapi.base.shop, ") {
		t.Errorf("output = %q, want a progress line while publicapi.base.shop builds", environment.output.String())
	}
}

func TestStartOfARunningDependencyDoesNotRebuildIt(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "internalapi")
	*environment.builds = nil
	environment.supervisor.restarted = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	for _, build := range *environment.builds {
		if build.workingDir == "/src/shop" {
			t.Errorf("rebuilt base although its internalapi already ran: %+v", build)
		}
	}
	if slices.Contains(environment.supervisor.restarted, "internalapi.base.shop") {
		t.Error("restarted base's internalapi")
	}
}

func TestStartStartsAgainADependencyWhoseProcessEnded(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "publicapi")
	environment.supervisor.setBehavior("internalapi.base.shop", processEndedAfterItWasReady)
	environment.supervisor.startedInOrder = nil

	environment.start(t, environment.base, StartOptions{}, "publicapi")

	if !slices.Contains(environment.supervisor.startedInOrder, "internalapi.base.shop") {
		t.Errorf("started %v, want internalapi.base.shop, whose process ended", environment.supervisor.startedInOrder)
	}
	if slices.Contains(environment.supervisor.startedInOrder, "scheduler.base.shop") || slices.Contains(environment.supervisor.restarted, "scheduler.base.shop") {
		t.Errorf("started %v, want scheduler.base.shop left alone: its process runs", environment.supervisor.startedInOrder)
	}
}

func TestStartKeepsACrashedDependencyInItsOwnStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.setBehavior("internalapi.base.shop", processEndedAfterItWasReady)
	environment.supervisor.restarted = nil
	environment.supervisor.startedInOrder = nil
	*environment.builds = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if !slices.Contains(environment.supervisor.startedInOrder, "internalapi.base.shop") {
		t.Errorf("started %v, want internalapi.base.shop started again in base", environment.supervisor.startedInOrder)
	}
	if _, isApplied := environment.supervisor.runningSpecs["internalapi.task-1234.shop"]; isApplied || slices.Contains(environment.supervisor.startedInOrder, "internalapi.task-1234.shop") {
		t.Errorf("started %v, want the crashed dependency kept in base, not moved to the feature stack", environment.supervisor.startedInOrder)
	}
	if !slices.ContainsFunc(*environment.builds, func(build recordedBuild) bool { return build.workingDir == "/src/shop" }) {
		t.Errorf("builds = %+v, want base built again before its internalapi restarts", *environment.builds)
	}
}

func TestStartStartsAgainADependencyTheSupervisorDoesNotKnow(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.forgetProcess("internalapi.base.shop")
	environment.supervisor.restarted = nil
	environment.supervisor.startedInOrder = nil
	*environment.builds = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if !slices.Contains(environment.supervisor.startedInOrder, "internalapi.base.shop") {
		t.Errorf("started %v, want internalapi.base.shop given back to the supervisor", environment.supervisor.startedInOrder)
	}
	if slices.Contains(environment.supervisor.restarted, "internalapi.base.shop") {
		t.Errorf("restarted %v, want no restart of a process the supervisor does not know", environment.supervisor.restarted)
	}
	if !slices.ContainsFunc(*environment.builds, func(build recordedBuild) bool { return build.workingDir == "/src/shop" }) {
		t.Errorf("builds = %+v, want base built again", *environment.builds)
	}
}

func TestStartDoesNotRestartARunningDependency(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.supervisor.restarted = nil
	*environment.builds = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if len(environment.supervisor.restarted) != 0 {
		t.Errorf("restarted %v, want nothing: every app of the start runs", environment.supervisor.restarted)
	}
	if len(*environment.builds) != 0 {
		t.Errorf("builds = %+v, want none: every app of the start runs", *environment.builds)
	}
}

func TestAFailedStartKeepsTheAppAMemberWithItsLog(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.setBehavior("publicapi.base.shop", processEndsAtEveryStart)
	logFile := filepath.Join(environment.executor.StackDir("base"), "logs", "publicapi.log")
	environment.supervisor.logBeforeReady = map[string]logLineBeforeReady{"publicapi.base.shop": {logPath: logFile, message: "pull access denied"}}

	err := environment.executor.Start(t.Context(), environment.base, []string{"publicapi"}, StartOptions{})

	if _, hasEnded := errors.AsType[*daemon.ExitedError](err); !hasEnded || !strings.Contains(err.Error(), "see "+logFile) {
		t.Fatalf("Start = %v, want the ended process and its log %s", err, logFile)
	}
	if !environment.isStoredAsRunning(t, "base", "publicapi") || !environment.isApplied("publicapi.base.shop") {
		t.Error("publicapi.base.shop ended, and it must stay a member that the daemon holds as ended: a failed start does not change ownership")
	}
	if !environment.isStoredAsRunning(t, "base", "internalapi") || !environment.isApplied("internalapi.base.shop") {
		t.Error("internalapi.base.shop started well in the same run but was stopped")
	}
	if _, err := os.Stat(logFile); err != nil {
		t.Errorf("the log of publicapi.base.shop is gone, but the error names it: %v", err)
	}
}

func TestARetryStartsTheFailedAppAgain(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.setBehavior("publicapi.base.shop", processEndsAtEveryStart)
	if err := environment.executor.Start(t.Context(), environment.base, []string{"publicapi"}, StartOptions{}); err == nil {
		t.Fatal("Start = nil, want the failure of publicapi.base.shop")
	}
	environment.supervisor.setBehavior("publicapi.base.shop", processEndedAfterItWasReady)
	environment.supervisor.startedInOrder = nil
	environment.supervisor.restarted = nil
	environment.supervisor.waitedReady = nil

	environment.start(t, environment.base, StartOptions{}, "publicapi")

	if want := []string{"publicapi.base.shop"}; !slices.Equal(environment.supervisor.startedInOrder, want) {
		t.Errorf("started %v, want only %v", environment.supervisor.startedInOrder, want)
	}
	if !slices.Contains(environment.supervisor.waitedReady, "publicapi.base.shop") || !environment.isStoredAsRunning(t, "base", "publicapi") {
		t.Error("publicapi.base.shop is not started and ready after the retry")
	}
	if slices.Contains(environment.supervisor.restarted, "internalapi.base.shop") {
		t.Errorf("restarted %v, want internalapi.base.shop left alone", environment.supervisor.restarted)
	}
}

func TestARetryStartsAFailedDependencyWithoutNamingIt(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.setBehavior("internalapi.base.shop", processEndsAtEveryStart)
	if err := environment.executor.Start(t.Context(), environment.featureStack, []string{"publicapi"}, StartOptions{}); err == nil {
		t.Fatal("Start = nil, want the failure of internalapi.base.shop")
	}
	environment.supervisor.setBehavior("internalapi.base.shop", processEndedAfterItWasReady)
	environment.supervisor.startedInOrder = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if !slices.Contains(environment.supervisor.startedInOrder, "internalapi.base.shop") || !environment.isApplied("publicapi.task-1234.shop") {
		t.Errorf("started %v, want internalapi.base.shop and publicapi.task-1234.shop", environment.supervisor.startedInOrder)
	}
	if environment.isApplied("internalapi.task-1234.shop") {
		t.Error("the retry moved internalapi into the feature stack")
	}
}

func TestARetryStartsAFailedFeatureStackDependencyInItsStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "internalapi", "publicapi")
	environment.supervisor.setBehavior("internalapi.task-1234.shop", processEndsAtEveryStart)
	if err := environment.executor.Start(t.Context(), environment.featureStack, []string{"publicapi"}, StartOptions{}); err == nil {
		t.Fatal("Start = nil, want the failure of internalapi.task-1234.shop")
	}
	environment.supervisor.setBehavior("internalapi.task-1234.shop", processEndedAfterItWasReady)
	environment.supervisor.startedInOrder = nil

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if !slices.Contains(environment.supervisor.startedInOrder, "internalapi.task-1234.shop") || slices.Contains(environment.supervisor.startedInOrder, "internalapi.base.shop") {
		t.Errorf("started %v, want internalapi started again in task-1234, which owns it", environment.supervisor.startedInOrder)
	}
}

func TestAFailedNamedAppOrBaseDependencyNeedsNoCommandToStartItAgain(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		failedProcess string
		appNames      []string
	}{
		{name: "named app in the feature stack", failedProcess: "internalapi.task-1234.shop", appNames: []string{"internalapi", "publicapi"}},
		{name: "dependency in base", failedProcess: "internalapi.base.shop", appNames: []string{"publicapi"}},
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
	environment.supervisor.setBehavior("internalapi.base.shop", processEndsWhileWaitedFor)

	err := environment.executor.Start(t.Context(), environment.featureStack, []string{"publicapi"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "internalapi.base.shop") {
		t.Fatalf("Start = %v, want the failure of internalapi.base.shop", err)
	}
	if !environment.isStoredAsRunning(t, "base", "internalapi") || !environment.isApplied("internalapi.base.shop") {
		t.Error("internalapi.base.shop ran before this start, which only waited for it, but it was stopped")
	}
}

func TestStartKeepsAnAppThatTimesOut(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.supervisor.setBehavior("publicapi.base.shop", processStaysNotReady)

	err := environment.executor.Start(t.Context(), environment.base, []string{"publicapi"}, StartOptions{})

	if err == nil || !strings.HasSuffix(err.Error(), "it still starts: ilot keeps it") {
		t.Fatalf("Start = %v, want it to say that the app still starts", err)
	}
	if !environment.isStoredAsRunning(t, "base", "publicapi") || !environment.isApplied("publicapi.base.shop") {
		t.Error("publicapi.base.shop still starts but was stopped")
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
				environment.supervisor.setBehavior("publicapi.base.shop", processEndsAtEveryStart)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			environment.supervisor.onWaitReady = func(processName string) {
				if processName == "publicapi.base.shop" {
					cancel()
				}
			}

			err := environment.executor.Start(ctx, environment.base, []string{"publicapi"}, StartOptions{})

			if err == nil {
				t.Fatal("Start = nil, want the cancelled wait")
			}
			if !environment.isStoredAsRunning(t, "base", "publicapi") || !environment.isApplied("publicapi.base.shop") {
				t.Error("publicapi.base.shop was stopped after a cancel")
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
	worktreePort := environment.appPort(t, "task-1234", "standalone")
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

	if _, isRunning := environment.supervisor.runningSpecs["internalapi.base.shop"]; isRunning {
		t.Error("internalapi.base.shop still runs")
	}
	if !strings.Contains(environment.output.String(), "publicapi.task-1234.shop") {
		t.Errorf("output %q does not warn about publicapi.task-1234.shop", environment.output.String())
	}
}

func TestStopDoesNotWarnAboutADependentThatItStopsToo(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "publicapi")
	environment.output.Reset()

	if err := environment.executor.Stop(t.Context(), environment.base, []string{"internalapi", "publicapi"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if strings.Contains(environment.output.String(), "warning") {
		t.Errorf("output %q, want no warning: publicapi stops with internalapi", environment.output.String())
	}
}

func TestTheProxyGetsARouteForEveryRoutedApp(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	route, isRouted := environment.supervisor.routeOf("publicapi.task-1234.shop.dev.example.net")
	if !isRouted || route.Port != environment.appPort(t, "task-1234", "publicapi") {
		t.Errorf("route of publicapi = %+v, routed %t; want its http port. Routes: %+v", route, isRouted, environment.supervisor.routes)
	}
	if want := "cd '/worktrees/task-1234' && ilot start publicapi"; route.StartHint != want {
		t.Errorf("start hint = %q, want %q", route.StartHint, want)
	}
	if _, isRouted := environment.supervisor.routeOf("router.base.shop.dev.example.net"); isRouted {
		t.Error("router.base.shop has a route, but the app declares none")
	}
}

func TestAStoppedAppKeepsItsRoute(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi", "standalone")

	if err := environment.executor.Stop(t.Context(), environment.featureStack, []string{"publicapi"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, isRouted := environment.supervisor.routeOf("publicapi.task-1234.shop.dev.example.net"); !isRouted {
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
	publicapiRow := rowFor(listing.Apps, "task-1234", "publicapi")
	if publicapiRow.Status != "ready" || publicapiRow.URL != "http://publicapi.task-1234.shop.dev.example.net:18080" {
		t.Errorf("publicapi row = %+v", publicapiRow)
	}
	if publicapiRow.Ports["http"] != environment.appPort(t, "task-1234", "publicapi") {
		t.Errorf("publicapi ports = %v, want the http port of its state", publicapiRow.Ports)
	}
	if schedulerRow := rowFor(listing.Apps, "base", "scheduler"); schedulerRow.Status != "ready" || schedulerRow.URL != "http://scheduler.base.shop.dev.example.net:18080" {
		t.Errorf("scheduler row = %+v", schedulerRow)
	}
	wantExternalServices := []ExternalServiceRow{{Name: "legacyapi", Status: "up", Port: 10004}, {Name: "secrets", Status: "up", Port: 8200}}
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

	if want := []string{"doc.base.shop"}; !slices.Equal(environment.supervisor.startedInOrder, want) {
		t.Errorf("started %v, want %v", environment.supervisor.startedInOrder, want)
	}
	if port := environment.appPort(t, "base", "doc"); port != 10007 {
		t.Errorf("doc port = %d, want its fixed 10007", port)
	}
}

func TestBaseRouterUsesItsFixedPortAndWorktreeRoutersDoNot(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	if port := environment.appPort(t, "base", "router"); port != 9090 {
		t.Errorf("base router port = %d, want 9090", port)
	}
	if port := environment.appPort(t, "task-1234", "router"); port < config.DefaultFirstPort || port > config.DefaultLastPort {
		t.Errorf("worktree router port = %d, want one from the reserved range", port)
	}
}

func TestStartRefusesAFixedPortHeldByAnotherProcess(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.IsPortFreeOnHost = func(port int) bool { return port != 9090 }

	err := environment.executor.Start(t.Context(), environment.base, []string{"router"}, StartOptions{})

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

	if got := environment.supervisor.runningSpecs["restapi.base.shop"].Dir; got != "/legacy/restapi" {
		t.Errorf("restapi WorkingDir = %q", got)
	}
	if got := environment.supervisor.runningSpecs["standalone.task-1234.shop"].Dir; got != "/worktrees/task-1234" {
		t.Errorf("standalone WorkingDir = %q", got)
	}
}

func TestWatchedAppRunsWithAWatcherThatRunsIlotRestart(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"PATH": "/usr/bin", "HOME": "/home/jdoe"})
	environment.executor.Machine.IlotEnv = map[string]string{"XDG_STATE_HOME": "/scratch/state"}

	environment.start(t, environment.base, StartOptions{}, "restapi")

	watcher, isRunning := environment.supervisor.runningSpecs["restapi-watch.base.shop"]
	if !isRunning {
		t.Fatalf("no watcher among %v", slices.Sorted(maps.Keys(environment.supervisor.runningSpecs)))
	}
	for _, wantPart := range []string{"watchexec --postpone", "--watch '/legacy/restapi'", "--exts 'pl,pm'", `-- ''"'"'/opt/ilot/bin/ilot'"'"' restart --workspace '"'"'shop'"'"' --stack '"'"'base'"'"' '"'"'restapi'"'"''`} {
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
	if _, isRunning := environment.supervisor.runningSpecs["restapi-watch.base.shop"]; isRunning {
		t.Error("the watcher outlives its app")
	}
}

func TestTheWatcherOfAFeatureStackNamesItsStack(t *testing.T) {
	environment := newTestEnvironment(t)
	studioStandalone := environment.executor.Config.Apps["studio-standalone"]
	studioStandalone.Watch = config.Watch{Paths: []string{"."}, Exts: []string{"go"}}
	environment.executor.Config.Apps["studio-standalone"] = studioStandalone

	environment.start(t, environment.featureStack, StartOptions{}, "studio-standalone")

	watcher := environment.supervisor.runningSpecs["studio-standalone-watch.task-1234.shop"]
	if !strings.Contains(watcher.Command, "--watch '/src/studio'") || !strings.Contains(watcher.Command, `restart --workspace '"'"'shop'"'"' --stack '"'"'task-1234'"'"' '"'"'studio-standalone'"'"'`) {
		t.Errorf("watcher command %q, want the main checkout of studio watched for the stack task-1234", watcher.Command)
	}
}

// Each save moves the last version to the backup, so a save with no change would lose the version before the last real change.
func TestASecondStartThatChangesNothingDoesNotSaveTheState(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "standalone")
	before, err := os.Stat(environment.executor.Store.File())
	if err != nil {
		t.Fatal(err)
	}

	environment.start(t, environment.base, StartOptions{}, "standalone")

	after, err := os.Stat(environment.executor.Store.File())
	if err != nil || !os.SameFile(before, after) {
		t.Errorf("a second start replaced %s (%v): want no save when nothing changed", environment.executor.Store.File(), err)
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

	if len(*environment.builds) != 1 || (*environment.builds)[0].workingDir != "/worktrees/task-1234" {
		t.Errorf("builds = %+v, want one build in the worktree", *environment.builds)
	}
	if want := []string{"restart internalapi.task-1234.shop", "ready internalapi.task-1234.shop"}; !slices.Equal(environment.supervisor.events, want) {
		t.Errorf("events = %v, want %v: the dependencies stay as they are", environment.supervisor.events, want)
	}
}

func TestRestartAppsKeepsTheRunningAppWhenItsBuildFails(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "internalapi")
	environment.supervisor.events = nil
	environment.executor.RunBuild = func(context.Context, string, string, []string) error { return errors.New("exit status 1") }

	err := environment.executor.RestartApps(t.Context(), environment.featureStack, []string{"internalapi"})

	if err == nil || !strings.Contains(err.Error(), "build internalapi.task-1234.shop") {
		t.Fatalf("RestartApps = %v, want the failed build", err)
	}
	if len(environment.supervisor.events) != 0 || !environment.isApplied("internalapi.task-1234.shop") {
		t.Errorf("events = %v, want none: the old version keeps running", environment.supervisor.events)
	}
}

func TestRestartAppsRefusesAnAppThatDoesNotRun(t *testing.T) {
	environment := newTestEnvironment(t)

	err := environment.executor.RestartApps(t.Context(), environment.featureStack, []string{"internalapi"})

	if err == nil || !strings.Contains(err.Error(), "internalapi.task-1234.shop does not run") || !strings.Contains(err.Error(), "ilot start internalapi") {
		t.Fatalf("RestartApps = %v, want a refusal that names ilot start", err)
	}
	if len(*environment.builds) != 0 || environment.supervisor.daemonIsUp {
		t.Errorf("builds = %+v, daemon up %t; want nothing done: a watcher must not start again an app that the user stopped", *environment.builds, environment.supervisor.daemonIsUp)
	}
}

// After ilot daemon stop, a restart would start the app with its dependencies down: ilot start starts them first.
func TestRestartAppsRefusesAnAppThatTheDaemonDoesNotHold(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	environment.supervisor.simulateDaemonRestart()
	*environment.builds = nil

	err := environment.executorOfANewCommand().RestartApps(t.Context(), environment.featureStack, []string{"standalone"})

	if err == nil || !strings.Contains(err.Error(), "standalone.task-1234.shop does not run: start it with ilot start standalone") {
		t.Fatalf("RestartApps = %v, want a refusal that names ilot start", err)
	}
	if len(*environment.builds) != 0 || len(environment.supervisor.events) != 0 {
		t.Errorf("builds = %+v, events = %v; want nothing done", *environment.builds, environment.supervisor.events)
	}
}

func TestRestartAppsRestartsAnAppThatExited(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	environment.supervisor.setBehavior("standalone.task-1234.shop", processEndedAfterItWasReady)
	environment.supervisor.events = nil

	if err := environment.executor.RestartApps(t.Context(), environment.featureStack, []string{"standalone"}); err != nil {
		t.Fatalf("RestartApps = %v, want a restart: the watcher of an app that crashed restarts it after a fix", err)
	}

	if len(environment.supervisor.events) == 0 {
		t.Errorf("events = %v, want the restart", environment.supervisor.events)
	}
}

func TestAnAppWithTheScopeBaseHasItsRouteInTheBaseStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "scheduler")

	if route, isRouted := environment.supervisor.routeOf("scheduler.base.shop.dev.example.net"); !isRouted || route.Port != 10012 {
		t.Errorf("route of scheduler = %+v, routed %t; want the scheduler UI on port 10012", route, isRouted)
	}
}

func TestStartAllKeepsAFailedAppAMemberAndStartsTheOthers(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "publicapi")
	environment.supervisor.simulateDaemonRestart()
	environment.supervisor.setBehavior("internalapi.base.shop", processEndsAtEveryStart)

	err := environment.executor.StartAll(t.Context())

	if err == nil || !strings.Contains(err.Error(), "internalapi.base.shop") {
		t.Fatalf("StartAll = %v, want an error naming internalapi.base.shop", err)
	}
	if !strings.Contains(err.Error(), "publicapi.base.shop") {
		t.Errorf("StartAll = %v, want it to name publicapi.base.shop as skipped", err)
	}
	started := environment.supervisor.startedInOrder
	for _, wantStarted := range []string{"scheduler.base.shop", "router.base.shop"} {
		if !slices.Contains(started, wantStarted) {
			t.Errorf("started %v, want %s despite the failure", started, wantStarted)
		}
	}
	if slices.Contains(started, "publicapi.base.shop") {
		t.Errorf("started %v, want publicapi.base.shop skipped because internalapi.base.shop failed", started)
	}
	if !environment.isStoredAsRunning(t, "base", "internalapi") || !environment.isStoredAsRunning(t, "base", "publicapi") {
		t.Error("a failed or skipped app lost its membership at a restore: the next ilot start would not start it again")
	}
	if !environment.isStoredAsRunning(t, "base", "scheduler") || !environment.isApplied("scheduler.base.shop") {
		t.Error("scheduler.base.shop started well at a restore but is not kept running")
	}
}

func TestStartAllSkipsAnAppWhoseDependencyFailedInTheFeatureStack(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "internalapi", "publicapi")
	environment.supervisor.simulateDaemonRestart()
	environment.supervisor.setBehavior("internalapi.task-1234.shop", processEndsAtEveryStart)

	err := environment.executor.StartAll(t.Context())

	if err == nil || !strings.Contains(err.Error(), "publicapi.task-1234.shop skipped: it depends on internalapi.task-1234.shop") {
		t.Fatalf("StartAll = %v, want publicapi.task-1234.shop skipped because internalapi.task-1234.shop failed", err)
	}
	if slices.Contains(environment.supervisor.startedInOrder, "publicapi.task-1234.shop") {
		t.Errorf("started %v, want publicapi.task-1234.shop skipped", environment.supervisor.startedInOrder)
	}
}

func TestStartAllSkipsAContainerWhoseDependencyImageFailed(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")
	environment.supervisor.simulateDaemonRestart()
	environment.executor.EnsureImage = (&imageRecorder{failing: map[string]string{"registry/search-cache:latest": "unauthorized"}}).ensure

	err := environment.executor.StartAll(t.Context())

	if err == nil || !strings.Contains(err.Error(), "search.task-1234.shop skipped: it depends on search-cache.task-1234.shop") {
		t.Fatalf("StartAll = %v, want search.task-1234.shop skipped because the image of search-cache.task-1234.shop failed", err)
	}
	if slices.Contains(environment.supervisor.startedInOrder, "search.task-1234.shop") {
		t.Errorf("started %v, want search.task-1234.shop skipped", environment.supervisor.startedInOrder)
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

	if route, _ := environment.supervisor.routeOf("publicapi.task-1234.shop.dev.example.net"); route.HomePath != "/swagger/" {
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
	if url := rowFor(listing.Apps, "task-1234", "publicapi").URL; url != "http://publicapi.task-1234.shop.dev.example.net:18080/swagger/" {
		t.Errorf("publicapi URL = %q", url)
	}
}

func TestLogFileOfAnAppIsInTheStackThatRunsIt(t *testing.T) {
	environment := newTestEnvironment(t)

	worktreeLog, err := environment.executor.LogFile(environment.featureStack, "publicapi")
	if err != nil {
		t.Fatalf("LogFile(publicapi): %v", err)
	}
	if want := filepath.Join(environment.executor.StackDir("task-1234"), "logs", "publicapi.log"); worktreeLog != want {
		t.Errorf("publicapi log = %q, want %q", worktreeLog, want)
	}
	baseLog, err := environment.executor.LogFile(environment.featureStack, "scheduler")
	if err != nil {
		t.Fatalf("LogFile(scheduler): %v", err)
	}
	if want := filepath.Join(environment.executor.StackDir("base"), "logs", "scheduler.log"); baseLog != want {
		t.Errorf("scheduler log = %q, want %q", baseLog, want)
	}
	if _, err := environment.executor.LogFile(environment.featureStack, "nope"); err == nil {
		t.Error("want an error for an unknown app")
	}
}

func TestAppRunsFromTheCheckoutOfItsRepo(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "studio-standalone", "standalone")

	studioSpec := environment.supervisor.runningSpecs["studio-standalone.task-1234.shop"]
	if studioSpec.Dir != "/src/studio" || !strings.HasSuffix(studioSpec.Command, "--from=/src/studio") {
		t.Errorf("studio-standalone spec = %+v, want studio's main checkout", studioSpec)
	}
	if got := environment.supervisor.runningSpecs["standalone.task-1234.shop"].Dir; got != "/worktrees/task-1234" {
		t.Errorf("standalone WorkingDir = %q, want shop's worktree", got)
	}
	if !slices.Contains(*environment.builds, recordedBuild{workingDir: "/src/studio", command: "go build ./cmd/api"}) {
		t.Errorf("builds = %+v, want the studio build in studio's checkout", *environment.builds)
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
	environment.executor.Config.RepoEnv = map[string]map[string]string{"shop": {"LOG_LEVEL": "info", "REGION": "DEV1"}}
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

	wantPort := strconv.Itoa(environment.appPort(t, "task-1234", "standalone"))
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
	if environment.supervisor.runningSpecs["standalone.task-1234.shop"].Env["LOG_LEVEL"] != "debug" {
		t.Errorf("the process does not get the environment: %v", environment.supervisor.runningSpecs["standalone.task-1234.shop"].Env)
	}
}

func TestTemplatesKnowTheStackTheUserRepoAndURL(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Machine.Login = "jdoe"
	environment.executor.Machine.ProxyPort = "18080"
	standalone := environment.executor.Config.Apps["standalone"]
	standalone.Run = `run {{.Stack}} {{.User}} {{repo "studio"}} {{url "scheduler"}} {{url "internalapi"}}`
	environment.executor.Config.Apps["standalone"] = standalone

	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	want := "run task-1234 jdoe /src/studio http://scheduler.base.shop.dev.example.net:18080 http://internalapi.base.shop.dev.example.net:18080"
	if got := environment.supervisor.runningSpecs["standalone.task-1234.shop"].Command; got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

func TestContainerCommandRemovesALeftoverContainerFirst(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")

	command := environment.supervisor.runningSpecs["search.task-1234.shop"].Command
	if !strings.HasPrefix(command, "docker rm -f 'ilot_search_task-1234_shop' >/dev/null 2>&1 || true && exec docker run --rm --name 'ilot_search_task-1234_shop' ") {
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

	command := environment.supervisor.runningSpecs["search.task-1234.shop"].Command
	searchPort := environment.appPort(t, "task-1234", "search")
	for _, wantPart := range []string{
		"--add-host 'host.docker.internal:host-gateway'",
		"-p 127.0.0.1:" + strconv.Itoa(searchPort) + ":16271",
		"--env-file '/etc/acme.conf.d/search.conf'",
		"-e 'REGION'",
		"-e 'ROUTER_URL'",
		"-v '/src/studio/schemas:/src/schemas'",
		"'--shm-size=1g' '--label=stack=task-1234' 'registry/search:latest'",
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
	if order := environment.supervisor.startedInOrder; slices.Index(order, "search-cache.task-1234.shop") > slices.Index(order, "search.task-1234.shop") {
		t.Errorf("started %v: want the cache before search", order)
	}
}

func TestAContainerReachesTheOtherAppsThroughTheNameOfTheMachine(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")

	searchEnv := environment.supervisor.runningSpecs["search.task-1234.shop"].Env
	wantRouter := "http://host.docker.internal:" + strconv.Itoa(environment.appPort(t, "task-1234", "router"))
	wantCache := "postgres://host.docker.internal:" + strconv.Itoa(environment.storedApp(t, "task-1234", "search-cache").Ports["postgres"]) + "/cache"
	if searchEnv["REGION"] != "DEV1" || searchEnv["ROUTER_URL"] != wantRouter || searchEnv["CACHE_URL"] != wantCache {
		t.Errorf("search env = %v, want %s for a process and %s for another container", searchEnv, wantRouter, wantCache)
	}
}

func TestAContainerIsProbedAndRoutedOnItsLoopbackPort(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")

	searchPort := environment.appPort(t, "task-1234", "search")
	if probe := environment.supervisor.runningSpecs["search.task-1234.shop"].Ready; probe == nil || probe.Host != "" || probe.Port != searchPort {
		t.Errorf("search probe = %+v, want the published port %d on the loopback address", probe, searchPort)
	}
	if probe := environment.supervisor.runningSpecs["search-cache.task-1234.shop"].Ready; probe == nil || probe.Command != "docker exec 'ilot_search-cache_task-1234_shop' sh -c 'pg_isready -U postgres'" {
		t.Errorf("cache probe = %+v", probe)
	}
	if route, isRouted := environment.supervisor.routeOf("search.task-1234.shop.dev.example.net"); !isRouted || route.Port != searchPort {
		t.Errorf("route of search = %+v, routed %t; want the published port %d", route, isRouted, searchPort)
	}
}

func TestAProcessReachesAContainerOnLocalhost(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "front-like")

	want := "front --api=http://localhost:" + strconv.Itoa(environment.appPort(t, "task-1234", "search")) + " --gw=localhost"
	if got := environment.supervisor.runningSpecs["front-like.task-1234.shop"].Command; got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

func TestTwoStacksPublishTheSameContainerOnTwoPorts(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "search")
	environment.start(t, environment.base, StartOptions{}, "search")

	featurePort, masterPort := environment.appPort(t, "task-1234", "search"), environment.appPort(t, "base", "search")
	if featurePort == masterPort {
		t.Errorf("both stacks publish search on port %d: the second container could not start", featurePort)
	}
}

func TestAContainerPortThatAnotherProgramHoldsIsNotGiven(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.IsPortFreeOnHost = func(port int) bool { return port != config.DefaultFirstPort }

	environment.start(t, environment.featureStack, StartOptions{SkipDependencies: true}, "search-cache")

	if port := environment.storedApp(t, "task-1234", "search-cache").Ports["postgres"]; port == config.DefaultFirstPort || port == 0 {
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
	if environment.supervisor.runningSpecs["front-like.task-1234.shop"].Command != want {
		t.Errorf("Command = %q, want %q", environment.supervisor.runningSpecs["front-like.task-1234.shop"].Command, want)
	}
}

func TestDomainTemplateGivesTheProxyDomain(t *testing.T) {
	environment := newTestEnvironment(t)
	standalone := environment.executor.Config.Apps["standalone"]
	standalone.Env = map[string]string{"ALLOWED_HOSTS": ".{{.Domain}}"}
	environment.executor.Config.Apps["standalone"] = standalone

	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	if got := environment.supervisor.runningSpecs["standalone.task-1234.shop"].Env["ALLOWED_HOSTS"]; got != ".dev.example.net" {
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

	if got := environment.supervisor.runningSpecs["standalone.task-1234.shop"].Env["EXPOSED_PORT"]; got != "18080" {
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

	if got, want := environment.supervisor.runningSpecs["standalone.task-1234.shop"].Stop, "docker stop standalone-task-1234"; got != want {
		t.Errorf("Stop = %q, want %q", got, want)
	}
}

func TestAContainerStopsWithDockerStopAndAProcessWithItsSignal(t *testing.T) {
	environment := newTestEnvironment(t)

	environment.start(t, environment.featureStack, StartOptions{}, "search")

	if got := environment.supervisor.runningSpecs["search-cache.task-1234.shop"].Stop; got != "docker stop -t 10 'ilot_search-cache_task-1234_shop'" {
		t.Errorf("search-cache Stop = %q", got)
	}
	if got := environment.supervisor.runningSpecs["router.task-1234.shop"].Stop; got != "" {
		t.Errorf("router Stop = %q, want none", got)
	}
}

func TestTheEnvOfTheRootRepoReachesTheAppsOfEveryRepoBelowTheEnvOfTheirOwnRepo(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Config.RepoEnv = map[string]map[string]string{
		"shop":   {"LOG_LEVEL": "info", "REGION": "DEV1", "STACK_NAME": "{{.Stack}}"},
		"studio": {"REGION": "EU2"},
	}
	environment.start(t, environment.featureStack, StartOptions{}, "studio-standalone")

	entryByKey := environmentOf(t, environment, "studio-standalone")

	want := map[string]plan.EnvEntry{
		"LOG_LEVEL":  {Key: "LOG_LEVEL", Value: "info", Source: "root repo"},
		"REGION":     {Key: "REGION", Value: "EU2", Source: "repo"},
		"STACK_NAME": {Key: "STACK_NAME", Value: "task-1234", Source: "computed"},
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
		"shop":   {"REGION": "{{.Stack}}", "DB_URL": "db-{{.Stack}}", "TOKEN": "{{.Stack}}"},
		"studio": {"DB_URL": "db-of-studio"},
	}
	studioStandalone := environment.executor.Config.Apps["studio-standalone"]
	studioStandalone.Env = map[string]string{"REGION": "EU1"}
	environment.executor.Config.Apps["studio-standalone"] = studioStandalone
	environment.executor.Settings = config.WorkspaceSettings{Repos: nil, Env: map[string]string{"DB_URL": "db-of-the-user", "TOKEN": "of-the-user"}, Apps: nil}
	environment.start(t, environment.featureStack, StartOptions{}, "studio-standalone")

	entryByKey := environmentOf(t, environment, "studio-standalone")

	want := map[string]plan.EnvEntry{
		"REGION": {Key: "REGION", Value: "EU1", Source: "app"},
		"DB_URL": {Key: "DB_URL", Value: "db-of-the-user", Source: "global"},
		"TOKEN":  {Key: "TOKEN", Value: "task-1234", Source: "computed"},
	}
	for key, wantEntry := range want {
		if entryByKey[key] != wantEntry {
			t.Errorf("%s = %+v, want %+v", key, entryByKey[key], wantEntry)
		}
	}
}

func TestAnUnknownAppNamesTheReposThatAreNotCloned(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	environment := newTestEnvironment(t)
	environment.executor.ReposNotCloned = map[string]string{"studio": "/home/user/go/src/studio"}

	err := environment.executor.Start(t.Context(), environment.featureStack, []string{"studio-front"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "workspaces.shop.repos.studio in ~/.config/ilot/config.yaml") || !strings.Contains(err.Error(), "/home/user/go/src/studio") {
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

	searchSpec := environment.supervisor.runningSpecs["search.task-1234.shop"]
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
	environment.executor.Config.RepoEnv = map[string]map[string]string{"shop": {"LOGKIT_LOGJSON": "1"}}
	environment.start(t, environment.base, StartOptions{}, "standalone")
	if got := environment.supervisor.runningSpecs["standalone.base.shop"].Env["LOGKIT_LOGJSON"]; got != "1" {
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

func TestStartAllSkipsTheAppWhoseImageFailsAndStartsTheOthers(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "front-like")
	environment.supervisor.simulateDaemonRestart()
	environment.executor.EnsureImage = (&imageRecorder{failing: map[string]string{"registry/search:latest": "unauthorized"}}).ensure

	err := environment.executor.StartAll(t.Context())

	if err == nil || !strings.Contains(err.Error(), "search.base.shop: pull registry/search:latest") || !strings.Contains(err.Error(), "front-like.base.shop skipped") {
		t.Fatalf("StartAll = %v, want the failed image named with its app and stack, and front-like.base.shop skipped", err)
	}
	started := environment.supervisor.startedInOrder
	if !slices.Contains(started, "search-cache.base.shop") || !slices.Contains(started, "router.base.shop") {
		t.Errorf("started %v, want the other apps started", started)
	}
	if slices.Contains(started, "search.base.shop") || slices.Contains(started, "front-like.base.shop") {
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
