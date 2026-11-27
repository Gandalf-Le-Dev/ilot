package plan

import "github.com/Gandalf-Le-Dev/ilot/internal/config"

// testConfig is a workspace with every kind of app: fixed ports, the scopes, containers, watchers and a second repo.
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
