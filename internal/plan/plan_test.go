package plan

import (
	"slices"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

const ticket = "retry-jobs"

func shopConfig() config.Config {
	return config.Config{
		External: map[string]config.ExternalService{"vault": {Port: 8200, StartHint: "start vault"}},
		Apps: map[string]config.App{
			"db":     {Run: "db", Ports: []string{"pg"}, FixedPorts: map[string]int{"pg": 5432}},
			"cache":  {Run: "cache", Scope: config.ScopeStack, Ports: []string{"redis"}},
			"auth":   {Run: "auth", Scope: config.ScopeBase, Ports: []string{"http"}},
			"api":    {Run: "api", Ports: []string{"http", "admin"}, DependsOn: []string{"db", "cache", "auth", "vault"}},
			"front":  {Run: "front", Ports: []string{"http"}, DependsOn: []string{"api"}},
			"single": {Run: "single"},
		},
	}
}

// snapshotOf gives a snapshot of the command of a stack where each instance in wanted is a member.
func snapshotOf(own string, configs map[string]config.Config, wanted ...Instance) Snapshot {
	snapshot := Snapshot{Workspace: "shop", Own: own, State: state.New(), Configs: configs, Checkouts: nil, ReposNotCloned: nil}
	for _, instance := range wanted {
		snapshot.SetWanted(instance, true)
	}
	return snapshot
}

func TestMembershipDecidesTheStackThatServesAnApp(t *testing.T) {
	configs := map[string]config.Config{ticket: shopConfig()}
	tests := []struct {
		name   string
		wanted []Instance
		app    string
		want   string
	}{
		{name: "an app that the stack does not own comes from base", wanted: nil, app: "db", want: BaseStack},
		{name: "an app that the stack owns comes from the stack", wanted: []Instance{{Stack: ticket, App: "db"}}, app: "db", want: ticket},
		{name: "an app with the scope stack always comes from the stack", wanted: nil, app: "cache", want: ticket},
		{name: "an app with the scope base always comes from base", wanted: []Instance{{Stack: ticket, App: "auth"}}, app: "auth", want: BaseStack},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := snapshotOf(ticket, configs, test.wanted...).Serving(ticket, test.app); got != test.want {
				t.Errorf("Serving(%s, %s) = %s, want %s", ticket, test.app, got, test.want)
			}
		})
	}
}

func TestEachInstanceUsesTheConfigurationOfItsOwnStack(t *testing.T) {
	baseConfig := shopConfig()
	ticketConfig := shopConfig()
	ticketConfig.Apps["api"] = config.App{Run: "api --retry", Ports: []string{"http"}}
	ticketConfig.Apps["iam"] = config.App{Run: "iam", Scope: config.ScopeBase}
	ticketConfig.Apps["search"] = config.App{Run: "search"}
	snapshot := snapshotOf(ticket, map[string]config.Config{BaseStack: baseConfig, ticket: ticketConfig})

	tests := []struct {
		instance     Instance
		wantRun      string
		wantDeclared bool
	}{
		{instance: Instance{Stack: ticket, App: "api"}, wantRun: "api --retry", wantDeclared: true},
		{instance: Instance{Stack: BaseStack, App: "api"}, wantRun: "api", wantDeclared: true},
		{instance: Instance{Stack: BaseStack, App: "iam"}, wantRun: "iam", wantDeclared: true},
		{instance: Instance{Stack: BaseStack, App: "search"}, wantRun: "", wantDeclared: false},
	}
	for _, test := range tests {
		app, isDeclared := snapshot.Declaration(test.instance)
		if app.Run != test.wantRun || isDeclared != test.wantDeclared {
			t.Errorf("Declaration(%v) = %q, %v; want %q, %v", test.instance, app.Run, isDeclared, test.wantRun, test.wantDeclared)
		}
	}
}

func TestStartOrderPutsEachDependencyFirstInTheStackThatServesIt(t *testing.T) {
	configs := map[string]config.Config{ticket: shopConfig()}
	tests := []struct {
		name          string
		requested     []string
		wanted        []Instance
		skip          bool
		wantOrder     []Instance
		wantExternals []string
	}{
		{
			name:          "dependencies come from base, and a scope stack dependency from the stack of its dependent",
			requested:     []string{"front"},
			wantOrder:     []Instance{{Stack: BaseStack, App: "db"}, {Stack: BaseStack, App: "cache"}, {Stack: BaseStack, App: "auth"}, {Stack: BaseStack, App: "api"}, {Stack: ticket, App: "front"}},
			wantExternals: []string{"vault"},
		},
		{
			name:          "a dependency that the command names runs in the stack",
			requested:     []string{"front", "api"},
			wantOrder:     []Instance{{Stack: BaseStack, App: "db"}, {Stack: ticket, App: "cache"}, {Stack: BaseStack, App: "auth"}, {Stack: ticket, App: "api"}, {Stack: ticket, App: "front"}},
			wantExternals: []string{"vault"},
		},
		{
			name:          "a dependency that the stack owns runs in the stack",
			requested:     []string{"front"},
			wanted:        []Instance{{Stack: ticket, App: "api"}},
			wantOrder:     []Instance{{Stack: BaseStack, App: "db"}, {Stack: ticket, App: "cache"}, {Stack: BaseStack, App: "auth"}, {Stack: ticket, App: "api"}, {Stack: ticket, App: "front"}},
			wantExternals: []string{"vault"},
		},
		{
			name:      "no dependency with skip",
			requested: []string{"front"},
			skip:      true,
			wantOrder: []Instance{{Stack: ticket, App: "front"}},
		},
		{
			name:      "an app with the scope base runs in base",
			requested: []string{"auth"},
			wantOrder: []Instance{{Stack: BaseStack, App: "auth"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := snapshotOf(ticket, configs, test.wanted...)
			requested, err := snapshot.Instances(ticket, test.requested)
			if err != nil {
				t.Fatal(err)
			}
			order, externals := snapshot.StartOrder(requested, test.skip)
			if !slices.Equal(order, test.wantOrder) || !slices.Equal(externals, test.wantExternals) {
				t.Errorf("StartOrder = %v, %v; want %v, %v", order, externals, test.wantOrder, test.wantExternals)
			}
		})
	}
}

func TestAnUnknownAppNamesTheKnownAppsAndTheReposThatAreNotCloned(t *testing.T) {
	snapshot := snapshotOf(ticket, map[string]config.Config{ticket: shopConfig()})
	snapshot.ReposNotCloned = map[string]string{"web": "/code/web"}

	_, err := snapshot.Instances(ticket, []string{"nope"})

	if err == nil || !containsAll(err.Error(), `unknown app "nope"`, "single", "repo web is not at /code/web", "workspaces.shop.repos.web") {
		t.Errorf("Instances = %v, want the known apps and the repo that is not cloned", err)
	}
}

func TestPlanStartMakesEveryInstanceAMemberAndListsThePortsToGive(t *testing.T) {
	snapshot := snapshotOf(ticket, map[string]config.Config{ticket: shopConfig()})
	snapshot.SetPort(Instance{Stack: BaseStack, App: "auth"}, "http", 20005)
	requested, err := snapshot.Instances(ticket, []string{"api"})
	if err != nil {
		t.Fatal(err)
	}

	start := snapshot.PlanStart(requested, false)

	for _, instance := range start.Order {
		if !start.After.IsWanted(instance) {
			t.Errorf("%v is not a member after the plan", instance)
		}
		if snapshot.IsWanted(instance) {
			t.Errorf("the plan changed the snapshot that it started from: %v is a member", instance)
		}
	}
	if want := []FixedPort{{Instance: Instance{Stack: BaseStack, App: "db"}, PortName: "pg", Port: 5432}}; !slices.Equal(start.FixedPorts, want) {
		t.Errorf("fixed ports = %v, want %v", start.FixedPorts, want)
	}
	wantNeeds := []PortNeed{
		{Instance: Instance{Stack: ticket, App: "cache"}, PortName: "redis"},
		{Instance: Instance{Stack: ticket, App: "api"}, PortName: "http"},
		{Instance: Instance{Stack: ticket, App: "api"}, PortName: "admin"},
	}
	if !slices.Equal(start.PortNeeds, wantNeeds) {
		t.Errorf("port needs = %v, want %v", start.PortNeeds, wantNeeds)
	}
}

func TestAFixedPortIsForTheBaseStackOnly(t *testing.T) {
	snapshot := snapshotOf(ticket, map[string]config.Config{ticket: shopConfig()})
	if ports := snapshot.FixedPorts(Instance{Stack: ticket, App: "db"}); len(ports) != 0 {
		t.Errorf("fixed ports of db in %s = %v, want none: two stacks would ask for one port", ticket, ports)
	}
	if ports := snapshot.FixedPorts(Instance{Stack: BaseStack, App: "db"}); ports["pg"] != 5432 {
		t.Errorf("fixed ports of db in base = %v, want pg 5432", ports)
	}
}

func containsAll(text string, parts ...string) bool {
	return !slices.ContainsFunc(parts, func(part string) bool { return !strings.Contains(text, part) })
}
