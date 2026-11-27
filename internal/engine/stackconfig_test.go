package engine

import (
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

func configWithApps(base config.Config, extraApps map[string]config.App) config.Config {
	config := base
	config.Apps = maps.Clone(base.Apps)
	maps.Copy(config.Apps, extraApps)
	return config
}

var featureOnlyApps = map[string]config.App{
	"feature-search": {
		Route:     "http",
		Container: config.Container{Image: "registry/feature-search:latest", Ports: map[string]int{"http": 16271}},
	},
	"feature-iam": {
		Scope:      "base",
		FixedPorts: map[string]int{"http": 8080},
		Route:      "http",
		Container:  config.Container{Image: "registry/iam:latest", Ports: map[string]int{"http": 8080}},
	},
}

func useConfigPerStack(environment testEnvironment, configByStack map[string]config.Config) {
	environment.executor.ConfigForStack = func(stackName string) (config.Config, error) {
		if config, isKnown := configByStack[stackName]; isKnown {
			return config, nil
		}
		return configByStack[workspace.BaseStackName], nil
	}
}

func TestACommandFromBaseKeepsTheAppsThatOnlyAFeatureDeclares(t *testing.T) {
	environment := newTestEnvironment(t)
	baseConfig := testConfig()
	featureConfig := configWithApps(baseConfig, featureOnlyApps)
	useConfigPerStack(environment, map[string]config.Config{workspace.BaseStackName: baseConfig, "task-1234": featureConfig})
	environment.executor.Config = featureConfig
	environment.start(t, environment.featureStack, StartOptions{}, "feature-search", "feature-iam")
	searchSpecBefore := environment.supervisor.runningSpecs["feature-search.task-1234.shop"]
	iamSpecBefore := environment.supervisor.runningSpecs["feature-iam.base.shop"]

	environment.executor.Config = baseConfig
	environment.start(t, environment.base, StartOptions{}, "standalone")

	if got := environment.supervisor.runningSpecs["feature-search.task-1234.shop"]; got.Command != searchSpecBefore.Command || got.Stop == "" {
		t.Errorf("feature-search spec after a base command = %+v, want %+v", got, searchSpecBefore)
	}
	if got := environment.supervisor.runningSpecs["feature-iam.base.shop"]; got.Command != iamSpecBefore.Command {
		t.Errorf("feature-iam command = %q, want %q", got.Command, iamSpecBefore.Command)
	}
	if _, isRouted := environment.supervisor.routeOf("feature-search.task-1234.shop.dev.example.net"); !isRouted {
		t.Errorf("routes = %+v, want the feature-search route kept", environment.supervisor.routes)
	}
}

func TestARunningAppThatNoConfigDeclaresAnyMoreIsStopped(t *testing.T) {
	environment := newTestEnvironment(t)
	baseConfig := testConfig()
	featureConfig := configWithApps(baseConfig, featureOnlyApps)
	configByStack := map[string]config.Config{workspace.BaseStackName: baseConfig, "task-1234": featureConfig}
	useConfigPerStack(environment, configByStack)
	environment.executor.Config = featureConfig
	environment.start(t, environment.featureStack, StartOptions{}, "feature-search")

	configByStack["task-1234"] = baseConfig
	environment.executor.Config = baseConfig
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")

	if _, isRunning := environment.supervisor.runningSpecs["feature-search.task-1234.shop"]; isRunning {
		t.Error("feature-search.task-1234.shop still runs, but no config declares it")
	}
	if !strings.Contains(environment.output.String(), "feature-search.task-1234.shop") {
		t.Errorf("output %q does not warn about the stopped app", environment.output.String())
	}
}

func TestEnvironmentPrintsNoWarningForAnAppThatNoConfigDeclares(t *testing.T) {
	environment := newTestEnvironment(t)
	baseConfig := testConfig()
	configByStack := map[string]config.Config{workspace.BaseStackName: baseConfig, "task-1234": configWithApps(baseConfig, featureOnlyApps)}
	useConfigPerStack(environment, configByStack)
	environment.executor.Config = configByStack["task-1234"]
	environment.start(t, environment.featureStack, StartOptions{}, "feature-search")
	configByStack["task-1234"] = baseConfig
	environment.executor.Config = baseConfig
	environment.output.Reset()
	environment.executor.ShellEnv = fixedShellEnv(map[string]string{"PATH": "/usr/bin"})

	entries, err := environment.executor.Environment(environment.featureStack, "standalone")

	if err != nil || len(entries) == 0 {
		t.Fatalf("Environment = %v, %v", entries, err)
	}
	if environment.output.Len() != 0 {
		t.Errorf("ilot env printed %q, want nothing: it changes nothing", environment.output.String())
	}
}

func TestABrokenConfigOfAStackDoesNotStopAStartInAnother(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	environment.executor.ConfigForStack = func(stackName string) (config.Config, error) {
		if stackName == "task-1234" {
			return config.Config{}, errors.New("parse ilot.yaml: bad indentation")
		}
		return testConfig(), nil
	}
	environment.supervisor.calls = nil

	environment.start(t, "retry-jobs", StartOptions{SkipDependencies: true}, "standalone")

	if slices.ContainsFunc(environment.supervisor.calls, func(call string) bool { return strings.Contains(call, "task-1234") }) {
		t.Errorf("calls = %v, want none for task-1234", environment.supervisor.calls)
	}
}

func TestAStartFailsWhenTheConfigOfBaseCannotBeRead(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.ConfigForStack = func(stackName string) (config.Config, error) {
		if stackName == "base" {
			return config.Config{}, errors.New("parse ilot.yaml: bad indentation")
		}
		return testConfig(), nil
	}

	err := environment.executor.Start(t.Context(), environment.featureStack, []string{"standalone"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "base") || !strings.Contains(err.Error(), "bad indentation") {
		t.Fatalf("Start = %v, want the config error of base, which serves the other apps", err)
	}
}

func TestStopWorksWithABrokenConfig(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.executor.Config = config.Config{}
	environment.executor.ConfigErr = errors.New("parse ilot.yaml: bad indentation")

	if err := environment.executor.Stop(t.Context(), environment.featureStack, []string{"publicapi", "scheduler"}); err != nil {
		t.Fatalf("Stop = %v, want the stop to work from the state alone", err)
	}

	for _, stoppedProcess := range []string{"publicapi.task-1234.shop", "scheduler.base.shop"} {
		if environment.isApplied(stoppedProcess) {
			t.Errorf("%s still runs after ilot stop", stoppedProcess)
		}
	}
	if !environment.isApplied("internalapi.base.shop") {
		t.Error("internalapi.base.shop stopped, but the stop did not name it")
	}
	if err := environment.executor.Stop(t.Context(), environment.featureStack, []string{"nope"}); err == nil || !strings.Contains(err.Error(), "bad indentation") {
		t.Errorf("Stop of an app that no stack has = %v, want the config error", err)
	}
}

func TestListNeedsNoConfig(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")
	environment.executor.ConfigForStack = func(string) (config.Config, error) {
		return config.Config{}, errors.New("repo shop has several worktrees for stack task-1234")
	}
	environment.executor.Config = config.Config{}
	environment.executor.ConfigErr = errors.New("parse ilot.yaml: bad indentation")
	environment.output.Reset()

	listing, err := environment.executor.List(t.Context())

	if err != nil {
		t.Fatalf("List = %v, want the rows of every stack", err)
	}
	row := rowFor(listing.Apps, "task-1234", "publicapi")
	if row.Status != "ready" || row.URL != "http://publicapi.task-1234.shop.dev.example.net" {
		t.Errorf("publicapi.task-1234 = %+v, want its status and its URL from the state", row)
	}
	if !strings.Contains(environment.output.String(), "bad indentation") {
		t.Errorf("output %q does not say why the external services are not listed", environment.output.String())
	}
}

func TestAnAppWithTheScopeBaseAndNoFixedPortGetsOnePortForEveryStack(t *testing.T) {
	environment := newTestEnvironment(t)
	doc := environment.executor.Config.Apps["doc"]
	doc.FixedPorts = nil
	environment.executor.Config.Apps["doc"] = doc
	reader := config.App{Run: `reader --doc={{port "doc"}}`, DependsOn: []string{"doc"}}
	environment.executor.Config.Apps["reader"] = reader

	environment.start(t, environment.featureStack, StartOptions{}, "reader")
	environment.start(t, environment.base, StartOptions{}, "reader")

	docPort := environment.appPort(t, "base", "doc")
	if docPort < config.DefaultFirstPort || docPort > config.DefaultLastPort {
		t.Errorf("doc port = %d, want a port of the range", docPort)
	}
	for _, processName := range []string{"reader.task-1234.shop", "reader.base.shop"} {
		if want := "reader --doc=" + strconv.Itoa(docPort); environment.supervisor.runningSpecs[processName].Command != want {
			t.Errorf("%s command = %q, want %q", processName, environment.supervisor.runningSpecs[processName].Command, want)
		}
	}
	if slices.Contains(environment.supervisor.startedInOrder, "doc.task-1234.shop") {
		t.Errorf("started %v, want doc in the base stack only", environment.supervisor.startedInOrder)
	}
}
