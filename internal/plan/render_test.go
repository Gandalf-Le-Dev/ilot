package plan

import (
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
)

func renderSnapshot() Snapshot {
	snapshot := snapshotOf(ticket, map[string]config.Config{ticket: shopConfig()}, Instance{Stack: ticket, App: "api"})
	snapshot.Checkouts = map[string]map[string]string{BaseStack: {"shop": "/code/shop"}, ticket: {"shop": "/code/shop-retry"}}
	snapshot.Machine = Machine{StateDir: "/state", Domain: "localhost", ProxyPort: "4580", Login: "me", ShellEnv: map[string]string{"PATH": "/usr/bin", "LOG": "shell"}, IlotBinary: "/bin/ilot", IlotEnv: nil}
	snapshot.Settings = config.WorkspaceSettings{Repos: nil, Env: map[string]string{"LOG": "global"}, Apps: nil}
	snapshot.SetPort(Instance{Stack: ticket, App: "api"}, "http", 20001)
	snapshot.SetPort(Instance{Stack: BaseStack, App: "db"}, "pg", 5432)
	return snapshot
}

func TestSpecRendersAnInstanceFromTheSnapshotAlone(t *testing.T) {
	snapshot := renderSnapshot()
	api := shopConfig().Apps["api"]
	api.Run = `api --port={{.Ports.http}} --db=localhost:{{port "db"}}`
	api.Env = map[string]string{"URL": `{{url "api"}}`, "LOG": "app"}
	snapshot.Configs[ticket].Apps["api"] = api

	spec, err := snapshot.Spec(Instance{Stack: ticket, App: "api"})

	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "api.retry-jobs.shop" || spec.Command != "api --port=20001 --db=localhost:5432" || spec.Dir != "/code/shop-retry" {
		t.Errorf("spec = %+v", spec)
	}
	if spec.Env["URL"] != "http://api.retry-jobs.shop.localhost:4580" || spec.Env["LOG"] != "global" || spec.Env["PATH"] != "/usr/bin" {
		t.Errorf("env = %v, want the URL of the stack, the global value over the app value, and the shell PATH", spec.Env)
	}
	if spec.LogFile != "/state/workspaces/shop/retry-jobs/logs/api.log" || spec.Ready == nil || spec.Ready.Port != 20001 {
		t.Errorf("log file %q and probe %+v", spec.LogFile, spec.Ready)
	}
}

func TestRoutesComeFromTheStateAndKeepAStoppedApp(t *testing.T) {
	snapshot := renderSnapshot()
	api := shopConfig().Apps["api"]
	api.Route = "http"
	snapshot.Configs[ticket].Apps["api"] = api
	snapshot.SetWanted(Instance{Stack: ticket, App: "api"}, false)

	routes := snapshot.Routes()

	if len(routes) != 1 || routes[0].Host != "api.retry-jobs.shop.localhost" || routes[0].Port != 20001 || routes[0].StartHint != "cd '/code/shop-retry' && ilot start api" {
		t.Errorf("routes = %+v, want the route of the stopped api with the command that starts it", routes)
	}
}
