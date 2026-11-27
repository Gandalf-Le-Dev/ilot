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

	if err == nil || !strings.Contains(err.Error(), "port "+strconv.Itoa(storedPort)) || !strings.Contains(err.Error(), "standalone.base.shop") {
		t.Fatalf("Start = %v, want the taken port %d of standalone.base.shop", err, storedPort)
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
	configByStack := map[string]config.Config{workspace.BaseStackName: baseConfig, "task-1234": configWithApps(baseConfig, featureOnlyApps)}
	useConfigPerStack(environment, configByStack)
	environment.executor.Config = configByStack["task-1234"]
	environment.start(t, environment.featureStack, StartOptions{}, "feature-search")
	searchPort := environment.storedApp(t, "task-1234", "feature-search").Ports["http"]
	configByStack["task-1234"] = baseConfig
	environment.executor.Config = baseConfig

	listing, err := environment.executor.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if row := rowFor(listing.Apps, "task-1234", "feature-search"); !row.Undeclared {
		t.Errorf("feature-search.task-1234 = %+v, want it marked undeclared", row)
	}
	if row := rowFor(listing.Apps, "base", "router"); row.Undeclared {
		t.Errorf("router.base = %+v, want it declared", row)
	}

	if err := environment.executor.Stop(t.Context(), environment.featureStack, []string{"feature-search"}); err != nil {
		t.Fatalf("Stop of an undeclared app = %v, want it removed", err)
	}

	current, err := environment.executor.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if current.HasApp("shop", "task-1234", "feature-search") || current.PortsInUse()[searchPort] {
		t.Errorf("feature-search is still in the state, or its port %d is still taken", searchPort)
	}
	if environment.isApplied("feature-search.task-1234.shop") {
		t.Error("feature-search.task-1234.shop still runs")
	}
}

// Another command can start a shared dependency while this one waits for the state lock.
func TestAStartSeesTheProcessesThatAnotherCommandStartedDuringItsWaitForTheLock(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "standalone")
	startedByTheOtherCommand := environment.supervisor.runningSpecs["standalone.base.shop"]
	if err := environment.executor.Stop(t.Context(), environment.base, []string{"standalone"}); err != nil {
		t.Fatal(err)
	}
	storedPort := environment.appPort(t, "base", "standalone")
	environment.executor.IsPortFreeOnHost = func(port int) bool {
		return port != storedPort || !environment.isApplied(startedByTheOtherCommand.Name)
	}
	release, err := environment.executor.Store.Lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	waits, mayGoOn := make(chan struct{}), make(chan struct{})
	environment.executor.Store.OnLockWait = func(string) {
		close(waits)
		<-mayGoOn
	}
	startErr := make(chan error)
	go func() {
		startErr <- environment.executor.Start(t.Context(), environment.base, []string{"standalone"}, StartOptions{})
	}()
	<-waits
	environment.supervisor.runningSpecs[startedByTheOtherCommand.Name] = startedByTheOtherCommand
	if err := release(); err != nil {
		t.Fatal(err)
	}
	close(mayGoOn)

	if err := <-startErr; err != nil {
		t.Fatalf("Start = %v, want no port test for a process that ilot runs", err)
	}
}
