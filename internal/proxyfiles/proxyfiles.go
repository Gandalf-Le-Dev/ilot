package proxyfiles

import (
	"cmp"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

type RoutedApp struct {
	Name     string
	Host     string
	Port     int
	HomePath string
}

type traefikRouter struct {
	Rule        string   `yaml:"rule"`
	Service     string   `yaml:"service"`
	Middlewares []string `yaml:"middlewares,omitempty"`
}

type traefikRedirectRegex struct {
	Regex       string `yaml:"regex"`
	Replacement string `yaml:"replacement"`
}

type traefikMiddleware struct {
	RedirectRegex traefikRedirectRegex `yaml:"redirectRegex"`
}

type traefikServer struct {
	URL string `yaml:"url"`
}

type traefikService struct {
	LoadBalancer struct {
		Servers []traefikServer `yaml:"servers"`
	} `yaml:"loadBalancer"`
}

func RoutesFileName(stackName string) string {
	return "dev-" + stackName + ".yml"
}

func TraefikRoutes(stackName, domain, backendHost string, routedApps []RoutedApp) ([]byte, error) {
	routers := map[string]traefikRouter{}
	services := map[string]traefikService{}
	middlewares := map[string]traefikMiddleware{}
	for _, app := range routedApps {
		routerName := app.Name + "-" + stackName
		hostRule := fmt.Sprintf("Host(`%s.%s.%s`)", app.Name, stackName, domain)
		routers[routerName] = traefikRouter{Rule: hostRule, Service: routerName, Middlewares: nil}
		if app.HomePath != "" {
			homeRouterName := routerName + "-home"
			routers[homeRouterName] = traefikRouter{Rule: hostRule + " && Path(`/`)", Service: routerName, Middlewares: []string{homeRouterName}}
			middlewares[homeRouterName] = traefikMiddleware{RedirectRegex: traefikRedirectRegex{Regex: "^(https?://[^/]+)/$", Replacement: "${1}" + app.HomePath}}
		}
		var service traefikService
		service.LoadBalancer.Servers = []traefikServer{{URL: fmt.Sprintf("http://%s:%d", cmp.Or(app.Host, backendHost), app.Port)}}
		services[routerName] = service
	}
	httpConfig := map[string]any{"routers": routers, "services": services}
	if len(middlewares) > 0 {
		httpConfig["middlewares"] = middlewares
	}
	return yaml.Marshal(map[string]any{"http": httpConfig})
}

func LocalServices(urlByGatewayAlias map[string]string) ([]byte, error) {
	content, err := json.MarshalIndent(urlByGatewayAlias, "", "    ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}
