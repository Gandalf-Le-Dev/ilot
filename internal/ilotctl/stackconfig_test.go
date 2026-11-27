package ilotctl

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/appconfig"
	"github.com/Gandalf-Le-Dev/ilot/internal/stack"
)

func configWithApps(base appconfig.Config, extraApps map[string]appconfig.App) appconfig.Config {
	config := base
	config.Apps = maps.Clone(base.Apps)
	maps.Copy(config.Apps, extraApps)
	return config
}

var ticketOnlyApps = map[string]appconfig.App{
	"ticket-search": {
		Ports:      []string{"http"},
		FixedPorts: map[string]int{"http": 16271},
		Route:      "http",
		Container:  appconfig.Container{Image: "registry/ticket-search:latest"},
	},
	"ticket-iam": {
		Shared:     true,
		Ports:      []string{"http"},
		FixedPorts: map[string]int{"http": 8080},
		Route:      "http",
		Container:  appconfig.Container{Image: "registry/iam:latest"},
	},
}

func useConfigPerStack(environment testEnvironment, configByStack map[string]appconfig.Config) {
	environment.controller.ConfigForStack = func(stackName string) (appconfig.Config, error) {
		if config, isKnown := configByStack[stackName]; isKnown {
			return config, nil
		}
		return configByStack[stack.MasterStackName], nil
	}
}

func TestACommandFromMasterKeepsTheAppsThatOnlyATicketDeclares(t *testing.T) {
	environment := newTestEnvironment(t)
	masterConfig := testConfig()
	ticketConfig := configWithApps(masterConfig, ticketOnlyApps)
	useConfigPerStack(environment, map[string]appconfig.Config{stack.MasterStackName: masterConfig, "proj-1234": ticketConfig})
	environment.controller.Config = ticketConfig
	environment.start(t, environment.featureStack, StartOptions{}, "ticket-search", "ticket-iam")
	searchSpecBefore := environment.supervisor.runningSpecs["ticket-search.proj-1234"]
	iamSpecBefore := environment.supervisor.runningSpecs["ticket-iam.devbox"]

	environment.controller.Config = masterConfig
	environment.start(t, environment.master, StartOptions{}, "standalone")

	if got := environment.supervisor.runningSpecs["ticket-search.proj-1234"]; got.Command != searchSpecBefore.Command || got.ShutdownCommand == "" {
		t.Errorf("ticket-search spec after a master command = %+v, want %+v", got, searchSpecBefore)
	}
	if got := environment.supervisor.runningSpecs["ticket-iam.devbox"]; got.Command != iamSpecBefore.Command {
		t.Errorf("shared ticket-iam command = %q, want %q", got.Command, iamSpecBefore.Command)
	}
	content, err := os.ReadFile(filepath.Join(environment.routesDir, "ilot-proj-1234.yml"))
	if err != nil || !strings.Contains(string(content), "ticket-search") {
		t.Errorf("routes of proj-1234 = %q, %v; want the ticket-search route kept", content, err)
	}
}

func TestARunningAppThatNoConfigDeclaresAnyMoreIsStopped(t *testing.T) {
	environment := newTestEnvironment(t)
	masterConfig := testConfig()
	ticketConfig := configWithApps(masterConfig, ticketOnlyApps)
	configByStack := map[string]appconfig.Config{stack.MasterStackName: masterConfig, "proj-1234": ticketConfig}
	useConfigPerStack(environment, configByStack)
	environment.controller.Config = ticketConfig
	environment.start(t, environment.featureStack, StartOptions{}, "ticket-search")

	configByStack["proj-1234"] = masterConfig
	environment.controller.Config = masterConfig
	environment.start(t, environment.master, StartOptions{}, "standalone")

	if _, isRunning := environment.supervisor.runningSpecs["ticket-search.proj-1234"]; isRunning {
		t.Error("ticket-search.proj-1234 still runs, but no config declares it")
	}
	if !strings.Contains(environment.output.String(), "ticket-search.proj-1234") {
		t.Errorf("output %q does not warn about the stopped app", environment.output.String())
	}
}

func TestAStackWhoseConfigCannotBeReadStopsTheCommand(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	environment.controller.ConfigForStack = func(stackName string) (appconfig.Config, error) {
		if stackName == "proj-1234" {
			return appconfig.Config{}, errors.New("parse ilot.yaml: bad indentation")
		}
		return testConfig(), nil
	}

	err := environment.controller.Start(t.Context(), environment.master, []string{"standalone"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "proj-1234") || !strings.Contains(err.Error(), "bad indentation") {
		t.Fatalf("Start = %v, want the config error of proj-1234", err)
	}
}

func TestListKeepsGoingWhenTheConfigOfAStackCannotBeRead(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.featureStack, StartOptions{}, "standalone")
	environment.controller.ConfigForStack = func(stackName string) (appconfig.Config, error) {
		if stackName == "proj-1234" {
			return appconfig.Config{}, errors.New("repo platform has several worktrees for stack proj-1234")
		}
		return testConfig(), nil
	}

	listing, err := environment.controller.List(t.Context())

	if err != nil {
		t.Fatalf("List = %v, want the rows of every stack", err)
	}
	if !slices.ContainsFunc(listing.Apps, func(row AppRow) bool { return row.Stack == "proj-1234" && row.App == "standalone" }) {
		t.Errorf("apps = %+v, want standalone.proj-1234", listing.Apps)
	}
	if !strings.Contains(environment.output.String(), "several worktrees") {
		t.Errorf("output %q does not warn about the stack whose config failed", environment.output.String())
	}
}
