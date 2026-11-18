package proxyfiles

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

type traefikDynamicConfig struct {
	HTTP struct {
		Routers map[string]struct {
			Rule    string `yaml:"rule"`
			Service string `yaml:"service"`
		} `yaml:"routers"`
		Services map[string]struct {
			LoadBalancer struct {
				Servers []struct {
					URL string `yaml:"url"`
				} `yaml:"servers"`
			} `yaml:"loadBalancer"`
		} `yaml:"services"`
	} `yaml:"http"`
}

func TestTraefikRoutesSendsEachAppHostToItsPort(t *testing.T) {
	content, err := TraefikRoutes("proj-1234", "dev.example.net", "devbox", []RoutedApp{
		{Name: "publicapi", Port: 20003},
		{Name: "internalapi", Port: 20001},
	})
	if err != nil {
		t.Fatalf("TraefikRoutes: %v", err)
	}

	var parsed traefikDynamicConfig
	if err := yaml.Unmarshal(content, &parsed); err != nil {
		t.Fatalf("output is not YAML: %v\n%s", err, content)
	}
	router := parsed.HTTP.Routers["publicapi-proj-1234"]
	if router.Rule != "Host(`publicapi.proj-1234.dev.example.net`)" {
		t.Errorf("rule = %q", router.Rule)
	}
	servers := parsed.HTTP.Services[router.Service].LoadBalancer.Servers
	if len(servers) != 1 || servers[0].URL != "http://devbox:20003" {
		t.Errorf("servers of %q = %+v", router.Service, servers)
	}
	if len(parsed.HTTP.Routers) != 2 {
		t.Errorf("routers = %v", parsed.HTTP.Routers)
	}
}

func TestRoutesFileNameDoesNotCollideWithCommittedRouteFiles(t *testing.T) {
	if got := RoutesFileName("master"); got != "dev-master.yml" {
		t.Errorf("RoutesFileName = %q", got)
	}
}

func TestLocalServicesMapsEachAliasToItsURL(t *testing.T) {
	content, err := LocalServices(map[string]string{
		"acme-internalapi": "http://localhost:20001",
	})
	if err != nil {
		t.Fatalf("LocalServices: %v", err)
	}
	var parsed map[string]string
	if err := json.Unmarshal(content, &parsed); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if parsed["acme-internalapi"] != "http://localhost:20001" || len(parsed) != 1 {
		t.Errorf("LocalServices = %v", parsed)
	}
}

func TestTraefikRoutesRedirectsOnlyTheRootOfAnAppWithAHomePath(t *testing.T) {
	content, err := TraefikRoutes("master", "dev.example.net", "devbox", []RoutedApp{
		{Name: "publicapi", Port: 20004, HomePath: "/swagger/"},
		{Name: "temporal", Port: 10012},
	})
	if err != nil {
		t.Fatalf("TraefikRoutes: %v", err)
	}
	var parsed struct {
		HTTP struct {
			Routers map[string]struct {
				Rule        string   `yaml:"rule"`
				Service     string   `yaml:"service"`
				Middlewares []string `yaml:"middlewares"`
			} `yaml:"routers"`
			Middlewares map[string]struct {
				RedirectRegex struct {
					Regex       string `yaml:"regex"`
					Replacement string `yaml:"replacement"`
				} `yaml:"redirectRegex"`
			} `yaml:"middlewares"`
		} `yaml:"http"`
	}
	if err := yaml.Unmarshal(content, &parsed); err != nil {
		t.Fatalf("output is not YAML: %v\n%s", err, content)
	}

	homeRouter, isPresent := parsed.HTTP.Routers["publicapi-master-home"]
	if !isPresent || homeRouter.Rule != "Host(`publicapi.master.dev.example.net`) && Path(`/`)" || homeRouter.Service != "publicapi-master" {
		t.Fatalf("home router = %+v (present %t)\n%s", homeRouter, isPresent, content)
	}
	if len(homeRouter.Middlewares) != 1 {
		t.Fatalf("home router middlewares = %v", homeRouter.Middlewares)
	}
	redirect := parsed.HTTP.Middlewares[homeRouter.Middlewares[0]].RedirectRegex
	if redirect.Regex != "^(https?://[^/]+)/$" || redirect.Replacement != "${1}/swagger/" {
		t.Errorf("redirect = %+v", redirect)
	}
	if _, isPresent := parsed.HTTP.Routers["temporal-master-home"]; isPresent {
		t.Error("temporal has no home path but got a home router")
	}
	if len(parsed.HTTP.Routers["publicapi-master"].Middlewares) != 0 {
		t.Error("the main router of publicapi redirects every path")
	}
}
