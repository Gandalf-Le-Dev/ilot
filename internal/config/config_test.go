package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const validConfigYAML = `
external:
  secrets: {port: 8200, start_hint: start it by hand}
apps:
  scheduler:
    scope: base
    run: scheduler server start-dev --ui-port {{.Ports.ui}}
    ports: [grpc, ui]
    fixed_ports: {grpc: 7233, ui: 10012}
    route: ui
    ready: {port: grpc}
  restapi:
    scope: base
    dir: /home/user/legacy/restapi
    run: perl main.pl prefork
    stop: perl main.pl stop
    ports: [http]
    fixed_ports: {http: 10003}
    route: http
    watch: {paths: [lib, main.pl], exts: [pl, pm]}
  router:
    run: routerd -listen-addr :{{.Ports.http}}
    ports: [http]
    fixed_ports: {http: 9090}
  internalapi:
    build: go build -o {{.BinDir}}/payments ./cmd/paymentsv2
    run: "{{.BinDir}}/payments api internalapi --port={{.Ports.http}}"
    env: {DEBUG: "1"}
    ports: [http, monitoring]
    route: http
    ready: {port: monitoring, path: /health}
    depends_on: [router, scheduler]
`

var defaultPorts = PortRange{First: DefaultFirstPort, Last: DefaultLastPort}

func Load(configPath string) (Config, error) {
	parsed, err := Parse(configPath)
	if err != nil {
		return Config{}, err
	}
	return parsed, parsed.Validate(defaultPorts)
}

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

	if config.External["secrets"].Port != 8200 {
		t.Errorf("secrets port = %d", config.External["secrets"].Port)
	}
	scheduler := config.Apps["scheduler"]
	if scheduler.Scope != ScopeBase || scheduler.FixedPorts["ui"] != 10012 {
		t.Errorf("scheduler = %+v", scheduler)
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

func TestParseOfAnUnknownKeyPointsToTheReadme(t *testing.T) {
	_, err := Parse(writeConfigFile(t, "apps:\n  web: {run: x, newfield: 1}\n"))
	if err == nil || !strings.Contains(err.Error(), "newfield") || !strings.Contains(err.Error(), "README") {
		t.Fatalf("err = %v, want the unknown key and where the keys are listed", err)
	}
}

func TestParseOfARemovedKeySaysWhatReplacedIt(t *testing.T) {
	testCases := []struct {
		yaml            string
		wantErrorSubstr string
	}{
		{yaml: "apps:\n  web: {run: x, gateway: true}\n", wantErrorSubstr: "gateway: it is gone: the port and url templates"},
		{yaml: "apps:\n  web: {run: x, temporal_namespace: true}\n", wantErrorSubstr: "temporal_namespace: it is gone"},
		{yaml: "gateway_base_file: gw.json\n", wantErrorSubstr: "gateway_base_file: it is gone"},
		{yaml: "apps:\n  db:\n    container: {image: x, links: {a: b}}\n", wantErrorSubstr: "links: it is gone: use the host and port templates"},
		{yaml: "repos:\n  studio: {path: ~/studio, config: ilot.yaml}\n", wantErrorSubstr: "studio: ../studio"},
		{yaml: "apps:\n  web: {run: x, shared: true}\n", wantErrorSubstr: "shared: write scope: base"},
		{yaml: "apps:\n  web: {run: x, per_stack: true}\n", wantErrorSubstr: "per_stack: write scope: stack"},
		{yaml: "apps:\n  web: {run: x, autostart: true}\n", wantErrorSubstr: "autostart: it is gone: ilot start with no app"},
		{yaml: "domain: dev.example.net\n", wantErrorSubstr: "domain: it moved to proxy.domain in the global file, ~/.config/ilot/config.yaml"},
	}
	for _, testCase := range testCases {
		_, err := Parse(writeConfigFile(t, testCase.yaml))
		if err == nil || !strings.Contains(err.Error(), testCase.wantErrorSubstr) {
			t.Errorf("Parse(%q) = %v, want an error with %q", testCase.yaml, err, testCase.wantErrorSubstr)
		}
	}
}

func TestParseOfBrokenYAMLHasNoHintAboutKeys(t *testing.T) {
	_, err := Parse(writeConfigFile(t, "apps: [\n"))
	if err == nil || strings.Contains(err.Error(), "README") {
		t.Fatalf("err = %v, want a parse error without the hint about keys", err)
	}
}

func TestParseOfAnEmptyFileGivesAnEmptyConfig(t *testing.T) {
	for _, content := range []string{"", "# apps come later\n"} {
		config, err := Parse(writeConfigFile(t, content))
		if err != nil || config.MemberOf != "" || len(config.Apps) != 0 {
			t.Errorf("Parse(%q) = %+v, %v; want an empty config", content, config, err)
		}
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
				"secrets": {Port: 7233},
				"other":   {Port: 7233},
			}},
			wantErrorSubstr: "port 7233",
		},
		{
			name: "duplicate fixed port between an app and an external service",
			config: Config{
				External: map[string]ExternalService{"secrets": {Port: 9090}},
				Apps:     map[string]App{"router": {Run: "gw", Ports: []string{"http"}, FixedPorts: map[string]int{"http": 9090}}},
			},
			wantErrorSubstr: "port 9090",
		},
		{
			name:            "external port inside the reserved range",
			config:          Config{External: map[string]ExternalService{"secrets": {Port: 20500}}},
			wantErrorSubstr: "inside the range 20000-29999",
		},
		{
			name:            "app fixed port inside the reserved range",
			config:          Config{Apps: map[string]App{"api": {Run: "api", Ports: []string{"http"}, FixedPorts: map[string]int{"http": 20500}}}},
			wantErrorSubstr: "inside the range 20000-29999",
		},
		{
			name:            "fixed port for an undeclared port",
			config:          Config{Apps: map[string]App{"api": {Run: "api", Ports: []string{"http"}, FixedPorts: map[string]int{"web": 10001}}}},
			wantErrorSubstr: `fixed port "web"`,
		},
		{
			name:            "a scope that does not exist",
			config:          Config{Apps: map[string]App{"scheduler": {Run: "scheduler", Scope: "shared"}}},
			wantErrorSubstr: `app "scheduler": scope "shared" is not base or stack`,
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
				External: map[string]ExternalService{"scheduler": {Port: 7233}},
				Apps:     map[string]App{"scheduler": {Run: "scheduler"}},
			},
			wantErrorSubstr: `"scheduler" is both`,
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
			err := testCase.config.Validate(defaultPorts)
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

func TestAnAppWithTheScopeBaseNeedsNoFixedPort(t *testing.T) {
	merged := Config{Apps: map[string]App{
		"mailpit": {Run: "mailpit", Scope: ScopeBase, Ports: []string{"smtp", "http"}, Route: "http"},
		"cache":   {Run: "cache", Scope: ScopeStack, Ports: []string{"tcp"}},
	}}
	if err := merged.Validate(defaultPorts); err != nil {
		t.Fatalf("Validate() = %v, want no error: ilot gives a port to an app of any scope", err)
	}
}

func TestParseReadsTheRepoListAndRepoEnv(t *testing.T) {
	config, err := Parse(writeConfigFile(t, "name: shop\nrepos:\n  studio: ../studio\nenv:\n  LOG_LEVEL: info\napps:\n  api: {run: api}\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if config.Name != "shop" || config.Repos["studio"] != "../studio" || config.Env["LOG_LEVEL"] != "info" {
		t.Errorf("config = %+v", config)
	}
}

func TestMergeSetsTheRepoOfEachApp(t *testing.T) {
	merged, err := Merge(map[string]Config{
		"payments": {Apps: map[string]App{"publicapi": {Run: "publicapi"}}},
		"studio":   {Env: map[string]string{"LOG_LEVEL": "info"}, Apps: map[string]App{"studio-api": {Run: "api"}}},
	}, defaultPorts)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if merged.Apps["publicapi"].Repo != "payments" || merged.Apps["studio-api"].Repo != "studio" {
		t.Errorf("apps = %+v", merged.Apps)
	}
	if merged.RepoEnv["studio"]["LOG_LEVEL"] != "info" {
		t.Errorf("merged = %+v", merged)
	}
}

func TestMergeRefusesAnAppDeclaredByTwoRepos(t *testing.T) {
	_, err := Merge(map[string]Config{
		"payments": {Apps: map[string]App{"api": {Run: "api"}}},
		"studio":   {Apps: map[string]App{"api": {Run: "api"}}},
	}, defaultPorts)
	if err == nil || !strings.Contains(err.Error(), `"api"`) || !strings.Contains(err.Error(), "studio") {
		t.Fatalf("err = %v, want an error naming the app and both repos", err)
	}
}

func TestValidateRefusesTopLevelPortsOnAContainer(t *testing.T) {
	config := Config{Apps: map[string]App{"search": {Ports: []string{"http"}, Container: Container{Image: "search:latest"}}}}
	err := config.Validate(defaultPorts)
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
	if err := config.Validate(defaultPorts); err != nil {
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
	if err := config.Validate(defaultPorts); err != nil {
		t.Fatalf("Validate() = %v, want two containers with the same port inside: each one is published on its own port", err)
	}
}

func TestValidateRefusesAContainerPortOutOfRange(t *testing.T) {
	config := Config{Apps: map[string]App{"search": {Container: Container{Image: "search:latest", Ports: map[string]int{"http": 0}}}}}
	err := config.Validate(defaultPorts)
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

func TestMergeKeepsTheWrapOfEachRepo(t *testing.T) {
	merged, err := Merge(map[string]Config{
		"payments": {Apps: map[string]App{"publicapi": {Run: "publicapi"}}},
		"studio":   {Wrap: "mise exec --", Apps: map[string]App{"front": {Run: "npm run dev"}}},
	}, defaultPorts)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(merged.RepoWrap) != 1 || merged.RepoWrap["studio"] != "mise exec --" {
		t.Errorf("RepoWrap = %v, want the prefix of studio only", merged.RepoWrap)
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
