package plan

import "github.com/Gandalf-Le-Dev/ilot/internal/config"

// testConfig is a workspace with every kind of app: fixed ports, the scopes, containers, watchers and a second repo.
func testConfig() config.Config {
	const buildPayments = "go build -o {{.BinDir}}/payments ./cmd/paymentsv2"
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
				Build:     buildPayments,
				Run:       "{{.BinDir}}/payments api internalapi --port={{.Ports.http}}",
				Env:       map[string]string{"ROUTER_URL": routerAddress, "SCHEDULER_ADDRESS": `localhost:{{port "scheduler" "grpc"}}`},
				Ports:     []string{"http"},
				Route:     "http",
				DependsOn: []string{"router", "scheduler"},
			},
			"publicapi": {
				Build:     buildPayments,
				Run:       "{{.BinDir}}/payments api publicapi --port={{.Ports.http}}",
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
