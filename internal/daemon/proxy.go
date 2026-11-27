package daemon

import (
	"cmp"
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type routeContextKey struct{}

// Proxy sends a request to the app that its host names. It reads the route table at each request.
type Proxy struct {
	routes  *RouteTable
	logger  *slog.Logger
	forward *httputil.ReverseProxy
}

func NewProxy(routes *RouteTable, logger *slog.Logger) *Proxy {
	logger = cmp.Or(logger, slog.New(slog.DiscardHandler))
	proxy := &Proxy{routes: routes, logger: logger}
	proxy.forward = &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			route := request.In.Context().Value(routeContextKey{}).(Route)
			// The name and not 127.0.0.1, because some dev servers listen on ::1 only.
			request.SetURL(&url.URL{Scheme: "http", Host: net.JoinHostPort("localhost", strconv.Itoa(route.Port))})
			// The app sees the host that the browser asked for, so its redirects and its host checks stay right.
			request.Out.Host = request.In.Host
			request.SetXForwarded()
		},
		Transport: &http.Transport{
			// The proxy settings of the environment must not send a loopback request to another machine.
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     30 * time.Second,
		},
		// A dev server streams events and logs: each write must reach the browser at once.
		FlushInterval: -1,
		ErrorHandler:  proxy.handleBackendError,
		ErrorLog:      slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	return proxy
}

func (proxy *Proxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	route, isKnown := proxy.routes.Lookup(hostWithoutPort(request.Host))
	if !isKnown {
		proxy.writeRouteList(writer, request)
		return
	}
	if route.HomePath != "" && request.URL.Path == "/" && request.URL.RawQuery == "" {
		http.Redirect(writer, request, route.HomePath, http.StatusFound)
		return
	}
	proxy.forward.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), routeContextKey{}, route)))
}

func hostWithoutPort(hostHeader string) string {
	if host, _, err := net.SplitHostPort(hostHeader); err == nil {
		return host
	}
	return hostHeader
}

func portSuffix(hostHeader string) string {
	if _, port, err := net.SplitHostPort(hostHeader); err == nil {
		return ":" + port
	}
	return ""
}

func (proxy *Proxy) handleBackendError(writer http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		// The browser went away. Nobody reads an answer.
		return
	}
	route := request.Context().Value(routeContextKey{}).(Route)
	if opError, isOpError := errors.AsType[*net.OpError](err); isOpError && opError.Op == "dial" {
		writePage(writer, http.StatusServiceUnavailable, notRunningPage, notRunningData{Host: route.Host, Port: route.Port, StartHint: route.StartHint})
		return
	}
	proxy.logger.Warn("the app broke the request", "host", route.Host, "port", route.Port, "error", err)
	http.Error(writer, "ilot: "+route.Host+" broke the request: "+err.Error(), http.StatusBadGateway)
}

type routeListData struct {
	Host   string
	Routes []routeLink
}

type routeLink struct {
	Host string
	URL  string
}

type notRunningData struct {
	Host      string
	Port      int
	StartHint string
}

func (proxy *Proxy) writeRouteList(writer http.ResponseWriter, request *http.Request) {
	data := routeListData{Host: hostWithoutPort(request.Host)}
	for _, route := range proxy.routes.All() {
		// The links carry the port of this request, because the proxy does not know which address the browser used.
		data.Routes = append(data.Routes, routeLink{Host: route.Host, URL: "http://" + route.Host + portSuffix(request.Host) + "/"})
	}
	writePage(writer, http.StatusNotFound, routeListPage, data)
}

func writePage(writer http.ResponseWriter, status int, page *template.Template, data any) {
	var body strings.Builder
	if err := page.Execute(&body, data); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte(body.String()))
}

var routeListPage = template.Must(template.New("routes").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>ilot</title></head>
<body>
<h1>ilot</h1>
<p>No app has the address <code>{{.Host}}</code>.</p>
{{if .Routes}}<p>The apps with an address:</p>
<ul>
{{range .Routes}}<li><a href="{{.URL}}">{{.Host}}</a></li>
{{end}}</ul>
{{else}}<p>No app has an address. Start one with <code>ilot start</code>.</p>
{{end}}</body>
</html>
`))

var notRunningPage = template.Must(template.New("not-running").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>{{.Host}} does not run</title></head>
<body>
<h1>{{.Host}} does not run</h1>
<p>Nothing answers on port {{.Port}}.</p>
{{if .StartHint}}<p>Start it with:</p>
<pre>{{.StartHint}}</pre>
{{end}}</body>
</html>
`))
