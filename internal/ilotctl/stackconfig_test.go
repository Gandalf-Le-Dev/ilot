package ilotctl

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

var ticketOnlyApps = map[string]config.App{
	"ticket-search": {
		Route:     "http",
		Container: config.Container{Image: "registry/ticket-search:latest", Ports: map[string]int{"http": 16271}},
	},
	"ticket-iam": {
		Scope:      "base",
		FixedPorts: map[string]int{"http": 8080},
		Route:      "http",
		Container:  config.Container{Image: "registry/iam:latest", Ports: map[string]int{"http": 8080}},
	},
}

func useConfigPerStack(environment testEnvironment, configByStack map[string]config.Config) {
	environment.controller.ConfigForStack = func(stackName string) (config.Config, error) {
		if config, isKnown := configByStack[stackName]; isKnown {
			return config, nil
		}
		return configByStack[workspace.BaseStackName], nil
	}
}

func TestACommandFromBaseKeepsTheAppsThatOnlyATicketDeclares(t *testing.T) {
	environment := newTestEnvironment(t)
	baseConfig := testConfig()
	ticketConfig := configWithApps(baseConfig, ticketOnlyApps)
	useConfigPerStack(environment, map[string]config.Config{workspace.BaseStackName: baseConfig, "proj-1234": ticketConfig})
	environment.controller.Config = ticketConfig
	environment.start(t, environment.featureStack, StartOptions{}, "ticket-search", "ticket-iam")
	searchSpecBefore := environment.supervisor.runningSpecs["ticket-search.proj-1234.platform"]
	iamSpecBefore := environment.supervisor.runningSpecs["ticket-iam.base.platform"]

	environment.controller.Config = baseConfig
	environment.start(t, environment.base, StartOptions{}, "standalone")

	if got := environment.supervisor.runningSpecs["ticket-search.proj-1234.platform"]; got.Command != searchSpecBefore.Command || got.Stop == "" {
		t.Errorf("ticket-search spec after a base command = %+v, want %+v", got, searchSpecBefore)
	}
	if got := environment.supervisor.runningSpecs["ticket-iam.base.platform"]; got.Command != iamSpecBefore.Command {
		t.Errorf("ticket-iam command = %q, want %q", got.Command, iamSpecBefore.Command)
	}
	if _, isRouted := environment.supervisor.routeOf("ticket-search.proj-1234.platform.dev.example.net"); !isRouted {
		t.Errorf("routes = %+v, want the ticket-search route kept", environment.supervisor.routes)
	}
}

func TestARunningAppThatNoConfigDeclaresAnyMoreIsStopped(t *testing.T) {
	environment := newTestEnvironment(t)
	baseConfig := testConfig()
	ticketConfig := configWithApps(baseConfig, ticketOnlyApps)
	configByStack := map[string]config.Config{workspace.BaseStackName: baseConfig, "proj-1234": ticketConfig}
	useConfigPerStack(environment, configByStack)
	environment.controller.Config = ticketConfig
	environment.start(t, environment.featureStack, StartOptions{}, "ticket-search")

	configByStack["proj-1234"] = baseConfig
	environment.controller.Config = baseConfig
	environment.start(t, environment.base, StartOptions{}, "standalone")

	if _, isRunning := environment.supervisor.runningSpecs["ticket-search.proj-1234.platform"]; isRunning {
		t.Error("ticket-search.proj-1234.platform still runs, but no config declares it")
	}
	if !strings.Contains(environment.output.String(), "ticket-search.proj-1234.platform") {
		t.Errorf("output %q does not warn about the stopped app", environment.output.String())
	}
}

func TestAStackWhoseConfigCannotBeReadStopsTheCommand(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	environment.controller.ConfigForStack = func(stackName string) (config.Config, error) {
		if stackName == "proj-1234" {
			return config.Config{}, errors.New("parse ilot.yaml: bad indentation")
		}
		return testConfig(), nil
	}

	err := environment.controller.Start(t.Context(), environment.base, []string{"standalone"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "proj-1234") || !strings.Contains(err.Error(), "bad indentation") {
		t.Fatalf("Start = %v, want the config error of proj-1234", err)
	}
}

func TestListKeepsGoingWhenTheConfigOfAStackCannotBeRead(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	environment.controller.ConfigForStack = func(stackName string) (config.Config, error) {
		if stackName == "proj-1234" {
			return config.Config{}, errors.New("repo platform has several worktrees for stack proj-1234")
		}
		return testConfig(), nil
	}

	listing, err := environment.controller.List(t.Context())

	if err != nil {
		t.Fatalf("List = %v, want the rows of every stack", err)
	}
	if !slices.ContainsFunc(listing.Apps, func(row AppRow) bool { return row.Stack == "proj-1234" && row.App == "standalone" }) {
		t.Errorf("apps = %+v, want standalone.proj-1234.platform", listing.Apps)
	}
	if !strings.Contains(environment.output.String(), "several worktrees") {
		t.Errorf("output %q does not warn about the stack whose config failed", environment.output.String())
	}
}

func TestAnAppWithTheScopeBaseAndNoFixedPortGetsOnePortForEveryStack(t *testing.T) {
	environment := newTestEnvironment(t)
	doc := environment.controller.Config.Apps["doc"]
	doc.FixedPorts = nil
	environment.controller.Config.Apps["doc"] = doc
	reader := config.App{Run: `reader --doc={{port "doc"}}`, DependsOn: []string{"doc"}}
	environment.controller.Config.Apps["reader"] = reader

	environment.start(t, environment.featureStack, StartOptions{}, "reader")
	environment.start(t, environment.base, StartOptions{}, "reader")

	docPort := environment.appPort(t, "base", "doc")
	if docPort < config.DefaultFirstPort || docPort > config.DefaultLastPort {
		t.Errorf("doc port = %d, want a port of the range", docPort)
	}
	for _, processName := range []string{"reader.proj-1234.platform", "reader.base.platform"} {
		if want := "reader --doc=" + strconv.Itoa(docPort); environment.supervisor.runningSpecs[processName].Command != want {
			t.Errorf("%s command = %q, want %q", processName, environment.supervisor.runningSpecs[processName].Command, want)
		}
	}
	if slices.Contains(environment.supervisor.startedInOrder, "doc.proj-1234.platform") {
		t.Errorf("started %v, want doc in the base stack only", environment.supervisor.startedInOrder)
	}
}
