package daemon

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
)

type Route struct {
	// The full host name, for example api.retry-jobs.shop.localhost.
	Host string `json:"host"`
	// The loopback port of the app.
	Port int `json:"port"`
	// A request for / goes to this path.
	HomePath string `json:"home_path,omitempty"`
	// The command that starts the app. The page for an app that does not run shows it.
	StartHint string `json:"start_hint,omitempty"`
}

// RouteTable is replaced as a whole, so a request never sees half of an update.
type RouteTable struct {
	byHost atomic.Pointer[map[string]Route]
}

func (table *RouteTable) Replace(routes []Route) error {
	byHost := make(map[string]Route, len(routes))
	for _, route := range routes {
		route.Host = strings.ToLower(route.Host)
		if route.Host == "" {
			return fmt.Errorf("a route has no host")
		}
		if route.Port < 1 || route.Port > 65535 {
			return fmt.Errorf("the route of %s has the port %d", route.Host, route.Port)
		}
		if route.HomePath != "" && !strings.HasPrefix(route.HomePath, "/") {
			return fmt.Errorf("the home path of %s does not start with a slash: %q", route.Host, route.HomePath)
		}
		if _, isDuplicate := byHost[route.Host]; isDuplicate {
			return fmt.Errorf("two routes have the host %s", route.Host)
		}
		byHost[route.Host] = route
	}
	table.byHost.Store(&byHost)
	return nil
}

func (table *RouteTable) Lookup(host string) (Route, bool) {
	byHost := table.byHost.Load()
	if byHost == nil {
		return Route{}, false
	}
	route, isKnown := (*byHost)[strings.ToLower(host)]
	return route, isKnown
}

func (table *RouteTable) All() []Route {
	byHost := table.byHost.Load()
	if byHost == nil {
		return nil
	}
	routes := make([]Route, 0, len(*byHost))
	for _, route := range *byHost {
		routes = append(routes, route)
	}
	slices.SortFunc(routes, func(left, right Route) int { return cmp.Compare(left.Host, right.Host) })
	return routes
}
