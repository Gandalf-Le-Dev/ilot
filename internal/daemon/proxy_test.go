package daemon

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func backendPort(t *testing.T, backend *httptest.Server) int {
	t.Helper()
	return backend.Listener.Addr().(*net.TCPAddr).Port
}

func namedBackend(t *testing.T, name string) *httptest.Server {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprintf(writer, "backend=%s host=%s uri=%s forwarded-host=%s", name, request.Host, request.URL.RequestURI(), request.Header.Get("X-Forwarded-Host"))
	}))
	t.Cleanup(backend.Close)
	return backend
}

func startProxy(t *testing.T, routes ...Route) *httptest.Server {
	t.Helper()
	table := &RouteTable{}
	if err := table.Replace(routes); err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(NewProxy(table, nil))
	t.Cleanup(front.Close)
	return front
}

// getThroughProxy asks the proxy for a host, as a browser does after it resolved the name to the loopback address.
func getThroughProxy(t *testing.T, front *httptest.Server, host, uri string) (status int, body string, header http.Header) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, front.URL+uri, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	client := &http.Client{
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(content), response.Header
}

func TestProxyRoutesByHost(t *testing.T) {
	api, web := namedBackend(t, "api"), namedBackend(t, "web")
	front := startProxy(t,
		Route{Host: "api.base.shop.localhost", Port: backendPort(t, api)},
		Route{Host: "web.retry-jobs.shop.localhost", Port: backendPort(t, web)},
	)

	status, body, _ := getThroughProxy(t, front, "api.base.shop.localhost:4580", "/v1/items?page=2")
	want := "backend=api host=api.base.shop.localhost:4580 uri=/v1/items?page=2 forwarded-host=api.base.shop.localhost:4580"
	if status != http.StatusOK || body != want {
		t.Errorf("got %d %q, want %q", status, body, want)
	}

	status, body, _ = getThroughProxy(t, front, "WEB.retry-jobs.shop.LOCALHOST", "/")
	if status != http.StatusOK || !strings.HasPrefix(body, "backend=web ") {
		t.Errorf("a host in capital letters and with no port gives %d %q", status, body)
	}
}

func TestProxyListsTheRoutesForAnUnknownHost(t *testing.T) {
	api := namedBackend(t, "api")
	front := startProxy(t,
		Route{Host: "web.base.shop.localhost", Port: 1},
		Route{Host: "api.base.shop.localhost", Port: backendPort(t, api)},
	)

	status, body, header := getThroughProxy(t, front, "nothing.shop.localhost:4580", "/some/path")
	if status != http.StatusNotFound || !strings.HasPrefix(header.Get("Content-Type"), "text/html") {
		t.Fatalf("got %d with the type %q", status, header.Get("Content-Type"))
	}
	apiLink := `<a href="http://api.base.shop.localhost:4580/">api.base.shop.localhost</a>`
	webLink := `<a href="http://web.base.shop.localhost:4580/">web.base.shop.localhost</a>`
	apiAt, webAt := strings.Index(body, apiLink), strings.Index(body, webLink)
	if apiAt < 0 || webAt < 0 || apiAt > webAt {
		t.Errorf("the page does not list both routes in order:\n%s", body)
	}
	if !strings.Contains(body, "nothing.shop.localhost") {
		t.Errorf("the page does not name the host of the request:\n%s", body)
	}

	_, body, _ = getThroughProxy(t, startProxy(t), "<script>.localhost", "/")
	if strings.Contains(body, "<script>") || !strings.Contains(body, "ilot start") {
		t.Errorf("the page of an empty table is wrong, or it does not escape the host:\n%s", body)
	}
}

func TestProxyAnswers503ForAnAppThatDoesNotRun(t *testing.T) {
	deadPort := freeLoopbackPort(t)
	front := startProxy(t, Route{Host: "api.base.shop.localhost", Port: deadPort, StartHint: "ilot start api --workspace shop --stack base"})

	status, body, _ := getThroughProxy(t, front, "api.base.shop.localhost:4580", "/")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("got status %d, want 503", status)
	}
	for _, want := range []string{"api.base.shop.localhost does not run", "ilot start api --workspace shop --stack base", fmt.Sprint(deadPort)} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not hold %q:\n%s", want, body)
		}
	}
}

func TestProxySendsTheRootToTheHomePath(t *testing.T) {
	api := namedBackend(t, "api")
	front := startProxy(t, Route{Host: "api.base.shop.localhost", Port: backendPort(t, api), HomePath: "/docs/index.html"})

	status, _, header := getThroughProxy(t, front, "api.base.shop.localhost:4580", "/")
	if status != http.StatusFound || header.Get("Location") != "/docs/index.html" {
		t.Errorf("the root gives %d with the location %q", status, header.Get("Location"))
	}
	for _, uri := range []string{"/health", "/?token=1"} {
		status, body, _ := getThroughProxy(t, front, "api.base.shop.localhost:4580", uri)
		if status != http.StatusOK || !strings.Contains(body, "uri="+uri+" ") {
			t.Errorf("%s gives %d %q, want the answer of the app", uri, status, body)
		}
	}
}

// The backend speaks the upgrade handshake and then echoes raw bytes. The proxy does not read WebSocket frames,
// so this covers what it adds: the upgrade and a tunnel in both directions.
func webSocketEchoBackend(t *testing.T) *httptest.Server {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.EqualFold(request.Header.Get("Upgrade"), "websocket") {
			http.Error(writer, "no upgrade header reached the app", http.StatusBadRequest)
			return
		}
		connection, buffered, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer connection.Close()
		_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = buffered.Flush()
		_, _ = io.Copy(connection, buffered)
	}))
	t.Cleanup(backend.Close)
	return backend
}

func TestProxyCarriesAWebSocket(t *testing.T) {
	backend := webSocketEchoBackend(t)
	front := startProxy(t, Route{Host: "api.base.shop.localhost", Port: backendPort(t, backend)})

	connection, err := net.Dial("tcp", front.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(testWait))
	_, err = io.WriteString(connection, "GET /socket HTTP/1.1\r\nHost: api.base.shop.localhost:4580\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols || !strings.EqualFold(response.Header.Get("Upgrade"), "websocket") {
		t.Fatalf("the upgrade gives %s with the header %q", response.Status, response.Header.Get("Upgrade"))
	}

	for _, message := range []string{"first message", "second message"} {
		if _, err := io.WriteString(connection, message); err != nil {
			t.Fatal(err)
		}
		echo := make([]byte, len(message))
		if _, err := io.ReadFull(reader, echo); err != nil {
			t.Fatalf("no echo of %q: %v", message, err)
		}
		if string(echo) != message {
			t.Fatalf("the echo of %q is %q", message, echo)
		}
	}
}

func TestProxyStreamsAResponse(t *testing.T) {
	releaseSecondChunk := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprintln(writer, "first")
		writer.(http.Flusher).Flush()
		select {
		case <-releaseSecondChunk:
		case <-request.Context().Done():
			return
		}
		fmt.Fprintln(writer, "second")
	}))
	defer backend.Close()
	front := startProxy(t, Route{Host: "api.base.shop.localhost", Port: backendPort(t, backend)})

	requestContext, cancel := context.WithTimeout(t.Context(), testWait)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, front.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "api.base.shop.localhost:4580"
	response, err := (&http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	lines := bufio.NewReader(response.Body)

	// The app holds the second line back. A proxy that waits for the whole body gives nothing here, and the context ends the test.
	first, err := lines.ReadString('\n')
	if err != nil || first != "first\n" {
		t.Fatalf("the first line did not arrive while the app still writes: %q, %v", first, err)
	}
	close(releaseSecondChunk)
	second, err := lines.ReadString('\n')
	if err != nil || second != "second\n" {
		t.Fatalf("the second line is %q, %v", second, err)
	}
}

func TestDaemonServesTheProxyAndItsRoutes(t *testing.T) {
	api := namedBackend(t, "api")
	options := Options{Paths: Paths{StateDir: shortDir(t)}, Version: "v-test", ProxyListen: "127.0.0.1:0"}
	runContext, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Run(runContext, options) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("the daemon returns %v", err)
		}
	})
	client := NewClient(options.Paths.Socket())
	var status Status
	waitUntil(t, "the daemon answers", func() bool {
		var err error
		status, err = client.Status(t.Context())
		return err == nil
	})
	if status.ProxyAddress == "" || strings.HasSuffix(status.ProxyAddress, ":0") {
		t.Fatalf("the status gives the proxy address %q", status.ProxyAddress)
	}
	front := &httptest.Server{URL: "http://" + status.ProxyAddress}

	if code, _, _ := getThroughProxy(t, front, "api.base.shop.localhost", "/"); code != http.StatusNotFound {
		t.Fatalf("before the routes arrive the proxy gives %d, want the list page", code)
	}
	if err := client.SetRoutes(t.Context(), []Route{{Host: "api.base.shop.localhost", Port: backendPort(t, api)}}); err != nil {
		t.Fatal(err)
	}
	code, body, _ := getThroughProxy(t, front, "api.base.shop.localhost", "/ping")
	if code != http.StatusOK || !strings.HasPrefix(body, "backend=api ") {
		t.Fatalf("after the routes arrive the proxy gives %d %q", code, body)
	}

	taken := options
	taken.Paths = Paths{StateDir: shortDir(t)}
	taken.ProxyListen = status.ProxyAddress
	secondContext, stopSecond := context.WithCancel(t.Context())
	secondDone := make(chan error, 1)
	go func() { secondDone <- Run(secondContext, taken) }()
	t.Cleanup(func() {
		stopSecond()
		if err := <-secondDone; err != nil {
			t.Errorf("the daemon with no proxy returns %v", err)
		}
	})
	secondClient := NewClient(taken.Paths.Socket())
	var secondStatus Status
	waitUntil(t, "the daemon whose proxy address is taken answers", func() bool {
		var err error
		secondStatus, err = secondClient.Status(t.Context())
		return err == nil
	})
	if secondStatus.ProxyAddress != "" || !strings.Contains(secondStatus.ProxyError, status.ProxyAddress) {
		t.Fatalf("a daemon whose proxy address is taken gives the status %+v", secondStatus)
	}
	if status.ProxyError != "" {
		t.Fatalf("a daemon whose proxy listens gives the error %q", status.ProxyError)
	}
}
