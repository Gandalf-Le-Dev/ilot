package appconfig

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const validConfigYAML = `
domain: dev.example.net
external:
  keyring: {port: 8200, start_hint: restart devbox}
apps:
  temporal:
    shared: true
    autostart: true
    run: temporal server start-dev --ui-port {{.Ports.ui}}
    ports: [grpc, ui]
    fixed_ports: {grpc: 7233, ui: 10012}
    route: ui
    ready: {port: grpc}
  restapi:
    shared: true
    dir: /home/user/legacy/restapi
    run: perl main.pl prefork
    stop: perl main.pl stop
    ports: [http]
    fixed_ports: {http: 10003}
    route: http
    watch: {paths: [lib, main.pl], exts: [pl, pm]}
  gateway:
    run: gatewayd -listen-addr :{{.Ports.http}}
    ports: [http]
    fixed_ports: {http: 9090}
  internalapi:
    build: go build -o {{.BinDir}}/platform ./cmd/platformv2
    run: "{{.BinDir}}/platform api internalapi --port={{.Ports.http}}"
    env: {DEBUG: "1"}
    ports: [http, monitoring]
    route: http
    ready: {port: monitoring, path: /health}
    depends_on: [gateway, temporal]
`

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "ilot.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestLoadReadsAppsAndExternalServices(t *testing.T) {
	config, err := Load(writeConfigFile(t, validConfigYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if config.Domain != "dev.example.net" {
		t.Errorf("Domain = %q", config.Domain)
	}
	if config.External["keyring"].Port != 8200 {
		t.Errorf("keyring port = %d", config.External["keyring"].Port)
	}
	temporal := config.Apps["temporal"]
	if !temporal.Shared || !temporal.Autostart || temporal.FixedPorts["ui"] != 10012 {
		t.Errorf("temporal = %+v", temporal)
	}
	restapi := config.Apps["restapi"]
	if restapi.Stop != "perl main.pl stop" {
		t.Errorf("restapi stop = %q", restapi.Stop)
	}
	if restapi.Dir != "/home/user/legacy/restapi" || len(restapi.Watch.Paths) != 2 || restapi.Watch.Exts[1] != "pm" {
		t.Errorf("restapi = %+v", restapi)
	}
	internalapi := config.Apps["internalapi"]
	if internalapi.Route != "http" || internalapi.Ready.Path != "/health" || internalapi.Env["DEBUG"] != "1" {
		t.Errorf("internalapi = %+v", internalapi)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	_, err := Load(writeConfigFile(t, "apps:\n  api:\n    rnu: true\n"))
	if err == nil || !strings.Contains(err.Error(), "rnu") {
		t.Fatalf("err = %v, want an error naming the unknown key", err)
	}
}

func TestParseOfAnUnknownFieldAsksForAPull(t *testing.T) {
	_, err := Parse(writeConfigFile(t, "apps:\n  web: {run: x, newfield: 1}\n"))
	if err == nil || !strings.Contains(err.Error(), "newfield") || !strings.HasSuffix(err.Error(), "\nyour platform main checkout can be older than this file: pull it") {
		t.Fatalf("err = %v, want the unknown field and the pull hint", err)
	}
}

func TestParseOfBrokenYAMLHasNoPullHint(t *testing.T) {
	_, err := Parse(writeConfigFile(t, "apps: [\n"))
	if err == nil || strings.Contains(err.Error(), "pull it") {
		t.Fatalf("err = %v, want a parse error without the pull hint", err)
	}
}

func TestValidateRejectsInvalidConfigs(t *testing.T) {
	testCases := []struct {
		name            string
		config          Config
		wantErrorSubstr string
	}{
		{
			name: "duplicate fixed port between external services",
			config: Config{External: map[string]ExternalService{
				"keyring": {Port: 7233},
				"other":   {Port: 7233},
			}},
			wantErrorSubstr: "port 7233",
		},
		{
			name: "duplicate fixed port between an app and an external service",
			config: Config{
				External: map[string]ExternalService{"keyring": {Port: 9090}},
				Apps:     map[string]App{"gateway": {Run: "gw", Ports: []string{"http"}, FixedPorts: map[string]int{"http": 9090}}},
			},
			wantErrorSubstr: "port 9090",
		},
		{
			name:            "external port inside the reserved range",
			config:          Config{External: map[string]ExternalService{"keyring": {Port: 20500}}},
			wantErrorSubstr: "reserved range",
		},
		{
			name:            "app fixed port inside the reserved range",
			config:          Config{Apps: map[string]App{"api": {Run: "api", Ports: []string{"http"}, FixedPorts: map[string]int{"http": 20500}}}},
			wantErrorSubstr: "reserved range",
		},
		{
			name:            "fixed port for an undeclared port",
			config:          Config{Apps: map[string]App{"api": {Run: "api", Ports: []string{"http"}, FixedPorts: map[string]int{"web": 10001}}}},
			wantErrorSubstr: `fixed port "web"`,
		},
		{
			name:            "shared app with a port that is not fixed",
			config:          Config{Apps: map[string]App{"temporal": {Run: "temporal", Shared: true, Ports: []string{"grpc", "ui"}, FixedPorts: map[string]int{"grpc": 7233}}}},
			wantErrorSubstr: `shared app "temporal" needs a fixed number for port "ui"`,
		},
		{
			name:            "stop command on a container app",
			config:          Config{Apps: map[string]App{"db": {Stop: "docker stop db", Container: Container{Image: "postgres:17"}}}},
			wantErrorSubstr: `app "db": stop is for an app with a run command`,
		},
		{
			name:            "home path without a route",
			config:          Config{Apps: map[string]App{"api": {Run: "api", HomePath: "/swagger/"}}},
			wantErrorSubstr: "home_path needs a route",
		},
		{
			name:            "home path not starting with a slash",
			config:          Config{Apps: map[string]App{"api": {Run: "api", Ports: []string{"http"}, Route: "http", HomePath: "swagger/"}}},
			wantErrorSubstr: `home_path "swagger/" must start with /`,
		},
		{
			name:            "watch without paths",
			config:          Config{Apps: map[string]App{"api": {Run: "api", Watch: Watch{Paths: nil, Exts: []string{"pl"}}}}},
			wantErrorSubstr: "watch needs paths",
		},
		{
			name:            "route names an undeclared port",
			config:          Config{Apps: map[string]App{"api": {Run: "api", Ports: []string{"http"}, Route: "web"}}},
			wantErrorSubstr: `route "web"`,
		},
		{
			name:            "readiness names an undeclared port",
			config:          Config{Apps: map[string]App{"api": {Run: "api", Ports: []string{"http"}, Ready: ReadyCheck{Port: "monitoring"}}}},
			wantErrorSubstr: `ready port "monitoring"`,
		},
		{
			name:            "unknown dependency",
			config:          Config{Apps: map[string]App{"api": {Run: "api", DependsOn: []string{"redis"}}}},
			wantErrorSubstr: `unknown dependency "redis"`,
		},
		{
			name: "dependency cycle",
			config: Config{Apps: map[string]App{
				"api":    {Run: "api", DependsOn: []string{"worker"}},
				"worker": {Run: "worker", DependsOn: []string{"api"}},
			}},
			wantErrorSubstr: "dependency cycle",
		},
		{
			name: "app and external service share a name",
			config: Config{
				External: map[string]ExternalService{"temporal": {Port: 7233}},
				Apps:     map[string]App{"temporal": {Run: "temporal"}},
			},
			wantErrorSubstr: `"temporal" is both`,
		},
		{
			name:            "app name is not a DNS label",
			config:          Config{Apps: map[string]App{"Go_API": {Run: "api"}}},
			wantErrorSubstr: "DNS label",
		},
		{
			name:            "app without a run command",
			config:          Config{Apps: map[string]App{"api": {}}},
			wantErrorSubstr: "run command",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.config.Validate()
			if err == nil || !strings.Contains(err.Error(), testCase.wantErrorSubstr) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, testCase.wantErrorSubstr)
			}
		})
	}
}

func TestValidateAcceptsValidConfig(t *testing.T) {
	if _, err := Load(writeConfigFile(t, validConfigYAML)); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestParseReadsTheRepoListAndRepoEnv(t *testing.T) {
	config, err := Parse(writeConfigFile(t, "repos:\n  portal: {path: ~/portal, config: ilot.yaml}\nenv:\n  LOG_LEVEL: info\napps:\n  api: {run: api}\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if config.Repos["portal"].Path != "~/portal" || config.Repos["portal"].Config != "ilot.yaml" || config.Env["LOG_LEVEL"] != "info" {
		t.Errorf("config = %+v", config)
	}
}

func TestMergeSetsTheRepoOfEachApp(t *testing.T) {
	merged, err := Merge(map[string]Config{
		"platform": {Domain: "dev.example.net", Apps: map[string]App{"publicapi": {Run: "publicapi"}}},
		"portal":   {Env: map[string]string{"LOG_LEVEL": "info"}, Apps: map[string]App{"portal-api": {Run: "api"}}},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if merged.Apps["publicapi"].Repo != "platform" || merged.Apps["portal-api"].Repo != "portal" {
		t.Errorf("apps = %+v", merged.Apps)
	}
	if merged.Domain != "dev.example.net" || merged.RepoEnv["portal"]["LOG_LEVEL"] != "info" {
		t.Errorf("merged = %+v", merged)
	}
}

func TestMergeRefusesAnAppDeclaredByTwoRepos(t *testing.T) {
	_, err := Merge(map[string]Config{
		"platform": {Apps: map[string]App{"api": {Run: "api"}}},
		"portal":   {Apps: map[string]App{"api": {Run: "api"}}},
	})
	if err == nil || !strings.Contains(err.Error(), `"api"`) || !strings.Contains(err.Error(), "portal") {
		t.Fatalf("err = %v, want an error naming the app and both repos", err)
	}
}

func TestValidateRefusesTopLevelPortsOnAContainer(t *testing.T) {
	config := Config{Apps: map[string]App{"search": {Ports: []string{"http"}, Container: Container{Image: "search:latest"}}}}
	err := config.Validate()
	if err == nil || !strings.Contains(err.Error(), `app "search": a container app declares its ports in container.ports`) {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestAContainerTakesItsPortNamesFromItsOwnBlock(t *testing.T) {
	config := Config{Apps: map[string]App{"search": {
		Build:     "make",
		Route:     "http",
		Ready:     ReadyCheck{Port: "admin"},
		Container: Container{Image: "search:latest", Ports: map[string]int{"http": 8080, "admin": 9000}},
	}}}
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want the route and the ready port found in container.ports", err)
	}
	if got := config.Apps["search"].PortNames(); !slices.Equal(got, []string{"admin", "http"}) {
		t.Errorf("PortNames = %v, want the sorted names of container.ports", got)
	}
}

func TestContainersMayUseTheSamePortInside(t *testing.T) {
	config := Config{Apps: map[string]App{
		"mock":   {Container: Container{Image: "mock:latest", Ports: map[string]int{"http": 8080}}},
		"search": {Container: Container{Image: "search:latest", Ports: map[string]int{"http": 8080}}},
	}}
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want two containers with the same port inside: each one is published on its own port", err)
	}
}

func TestValidateRefusesAContainerPortOutOfRange(t *testing.T) {
	config := Config{Apps: map[string]App{"search": {Container: Container{Image: "search:latest", Ports: map[string]int{"http": 0}}}}}
	err := config.Validate()
	if err == nil || !strings.Contains(err.Error(), `container port "http" has the number 0`) {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestParseReadsTheContainerBlockAndRefusesTheOldLinks(t *testing.T) {
	config, err := Parse(writeConfigFile(t, `apps:
  db:
    container:
      image: postgres:17
      ports: {pg: 5432}
      mounts: {"{{.StackDir}}/pg": /var/lib/postgresql/data}
      env_files: [.env.db]
      args: [--shm-size=1g]
    ready: {port: pg}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	container := config.Apps["db"].Container
	if container.Ports["pg"] != 5432 || container.Mounts["{{.StackDir}}/pg"] != "/var/lib/postgresql/data" || !slices.Equal(container.Args, []string{"--shm-size=1g"}) {
		t.Errorf("container = %+v", container)
	}

	_, err = Parse(writeConfigFile(t, "apps:\n  db:\n    container:\n      image: postgres:17\n      links: {cache: redis}\n"))
	if err == nil || !strings.Contains(err.Error(), "links") {
		t.Errorf("Parse of a file with links = %v, want an error that names the removed key", err)
	}
}

func TestMergeGivesTheLocalhostDomainWhenNoFileSetsOne(t *testing.T) {
	merged, err := Merge(map[string]Config{"platform": {Apps: map[string]App{"publicapi": {Run: "publicapi"}}}})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if merged.Domain != "localhost" {
		t.Errorf("Domain = %q, want localhost, which needs no DNS", merged.Domain)
	}
}

func TestMergeKeepsTheWrapOfEachRepo(t *testing.T) {
	merged, err := Merge(map[string]Config{
		"platform": {Apps: map[string]App{"publicapi": {Run: "publicapi"}}},
		"portal":   {Wrap: "mise exec --", Apps: map[string]App{"front": {Run: "npm run dev"}}},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(merged.RepoWrap) != 1 || merged.RepoWrap["portal"] != "mise exec --" {
		t.Errorf("RepoWrap = %v, want the prefix of portal only", merged.RepoWrap)
	}
}

func TestParseReadsTheWrapOfARepo(t *testing.T) {
	config, err := Parse(writeConfigFile(t, "wrap: mise exec --\napps:\n  front:\n    run: npm run dev\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if config.Wrap != "mise exec --" {
		t.Errorf("Wrap = %q", config.Wrap)
	}
}
