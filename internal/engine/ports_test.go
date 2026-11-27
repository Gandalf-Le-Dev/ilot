package engine

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

func TestAStartRefusesAStoredPortThatAnotherProgramTook(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "standalone")
	if err := environment.executor.Stop(t.Context(), environment.base, []string{"standalone"}); err != nil {
		t.Fatal(err)
	}
	storedPort := environment.appPort(t, "base", "standalone")
	environment.executor.IsPortFreeOnHost = func(port int) bool { return port != storedPort }
	environment.supervisor.startedInOrder = nil

	err := environment.executor.Start(t.Context(), environment.base, []string{"standalone"}, StartOptions{})

	if err == nil || !strings.Contains(err.Error(), "port "+strconv.Itoa(storedPort)) || !strings.Contains(err.Error(), "standalone.base.platform") {
		t.Fatalf("Start = %v, want the taken port %d of standalone.base.platform", err, storedPort)
	}
	if len(environment.supervisor.startedInOrder) != 0 {
		t.Errorf("started %v before the refusal", environment.supervisor.startedInOrder)
	}
}

func TestAStartDoesNotTestThePortsOfAnAppThatRuns(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "standalone")
	storedPort := environment.appPort(t, "base", "standalone")
	environment.executor.IsPortFreeOnHost = func(port int) bool { return port != storedPort }

	environment.start(t, environment.base, StartOptions{}, "standalone")
}

func TestStopOfAnAppThatNoConfigDeclaresFreesItsPorts(t *testing.T) {
	environment := newTestEnvironment(t)
	baseConfig := testConfig()
	configByStack := map[string]config.Config{workspace.BaseStackName: baseConfig, "proj-1234": configWithApps(baseConfig, ticketOnlyApps)}
	useConfigPerStack(environment, configByStack)
	environment.executor.Config = configByStack["proj-1234"]
	environment.start(t, environment.featureStack, StartOptions{}, "ticket-search")
	searchPort := environment.storedApp(t, "proj-1234", "ticket-search").Ports["http"]
	configByStack["proj-1234"] = baseConfig
	environment.executor.Config = baseConfig

	listing, err := environment.executor.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if row := rowFor(listing.Apps, "proj-1234", "ticket-search"); !row.Undeclared {
		t.Errorf("ticket-search.proj-1234 = %+v, want it marked undeclared", row)
	}
	if row := rowFor(listing.Apps, "base", "gateway"); row.Undeclared {
		t.Errorf("gateway.base = %+v, want it declared", row)
	}

	if err := environment.executor.Stop(t.Context(), environment.featureStack, []string{"ticket-search"}); err != nil {
		t.Fatalf("Stop of an undeclared app = %v, want it removed", err)
	}

	current, err := environment.executor.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if current.HasApp("platform", "proj-1234", "ticket-search") || current.PortsInUse()[searchPort] {
		t.Errorf("ticket-search is still in the state, or its port %d is still taken", searchPort)
	}
	if environment.isApplied("ticket-search.proj-1234.platform") {
		t.Error("ticket-search.proj-1234.platform still runs")
	}
}
