package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortDir exists because a unix socket path holds 104 bytes at most on macOS, and t.TempDir is longer than that.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ilot")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

type socketServer struct {
	client       *Client
	supervisor   *Supervisor
	routes       *RouteTable
	shutdownSeen chan struct{}
}

func serveOnSocket(t *testing.T) *socketServer {
	t.Helper()
	socketPath := filepath.Join(shortDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	served := &socketServer{
		client:       NewClient(socketPath),
		supervisor:   newTestSupervisor(t, quickProbes),
		routes:       &RouteTable{},
		shutdownSeen: make(chan struct{}),
	}
	api := &Server{
		Supervisor: served.supervisor,
		Routes:     served.routes,
		Status:     Status{Version: "v-test", PID: os.Getpid(), StartedAt: time.Unix(1_800_000_000, 0).UTC(), ProxyAddress: "127.0.0.1:4580"},
		OnShutdown: func() { close(served.shutdownSeen) },
	}
	httpServer := &http.Server{Handler: api.Handler()}
	serving := make(chan struct{})
	go func() {
		defer close(serving)
		_ = httpServer.Serve(listener)
	}()
	t.Cleanup(func() {
		httpServer.Close()
		<-serving
	})
	return served
}

func TestRoundTripOfEveryCall(t *testing.T) {
	served := serveOnSocket(t)
	client := served.client
	ctx := t.Context()
	workingDir := t.TempDir()

	status, err := client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := Status{Version: "v-test", PID: os.Getpid(), StartedAt: time.Unix(1_800_000_000, 0).UTC(), ProxyAddress: "127.0.0.1:4580"}
	if status != want {
		t.Fatalf("status is %+v, want %+v", status, want)
	}

	spec := Spec{
		Command: "sleep 300",
		Dir:     workingDir,
		Env:     map[string]string{"PATH": "/usr/bin:/bin"},
		LogFile: filepath.Join(workingDir, "api.log"),
		Ready:   &Probe{Command: "test -f ready.flag"},
	}
	const name = "api.retry-jobs.shop"
	started, err := client.Put(ctx, name, spec)
	if err != nil {
		t.Fatal(err)
	}
	if started.Name != name || started.State != StateStarting || started.PID == 0 || started.Hash != spec.Hash() {
		t.Fatalf("PUT gives %+v", started)
	}

	shortWait, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	err = client.WaitReady(shortWait, name)
	cancel()
	notReady, isNotReady := errors.AsType[*NotReadyError](err)
	if !isNotReady || notReady.State != StateStarting || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a short wait gives %v, want a NotReadyError", err)
	}
	if err := os.WriteFile(filepath.Join(workingDir, "ready.flag"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := client.WaitReady(ctx, name); err != nil {
		t.Fatal(err)
	}

	processes, err := client.Processes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(processes) != 1 || processes[0].State != StateReady || processes[0].PID != started.PID {
		t.Fatalf("GET processes gives %+v", processes)
	}

	same, err := client.Put(ctx, name, spec)
	if err != nil {
		t.Fatal(err)
	}
	if same.PID != started.PID {
		t.Fatalf("PUT of the same specification restarted the process")
	}

	restarted, err := client.Restart(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.PID == started.PID || restarted.Hash != started.Hash {
		t.Fatalf("restart gives %+v after %+v", restarted, started)
	}

	routes := []Route{{Host: "api.retry-jobs.shop.localhost", Port: 20001, HomePath: "/docs", StartHint: "ilot start api"}}
	if err := client.SetRoutes(ctx, routes); err != nil {
		t.Fatal(err)
	}
	if got := served.routes.All(); len(got) != 1 || got[0] != routes[0] {
		t.Fatalf("the route table holds %+v", got)
	}

	if err := client.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	if processes, _ := client.Processes(ctx); len(processes) != 0 {
		t.Fatalf("after DELETE the daemon still has %+v", processes)
	}
	waitUntil(t, "the deleted process is dead", func() bool { return !isProcessAlive(restarted.PID) })

	survivor, err := client.Put(ctx, "worker", Spec{Command: "sleep 300", Env: spec.Env, LogFile: filepath.Join(workingDir, "worker.log")})
	if err != nil {
		t.Fatal(err)
	}
	// Not Client.Shutdown: it waits for the end of the daemon, and this server stays to answer the calls below.
	if err := client.call(ctx, http.MethodPost, "/v1/shutdown", nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-served.shutdownSeen:
	case <-time.After(testWait):
		t.Fatal("the shutdown call did not reach OnShutdown")
	}
	waitUntil(t, "shutdown stopped the process", func() bool { return !isProcessAlive(survivor.PID) })
	if _, err := client.Put(ctx, "late", spec); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("a PUT after the shutdown gives %v", err)
	}
	if status, err := client.Status(ctx); err != nil || !status.ShuttingDown {
		t.Fatalf("after the shutdown the status is %+v, %v", status, err)
	}
}

func TestClientErrors(t *testing.T) {
	served := serveOnSocket(t)
	client := served.client
	ctx := t.Context()

	if _, err := client.Restart(ctx, "unknown"); !errors.Is(err, ErrUnknownProcess) {
		t.Errorf("a restart of an unknown process gives %v", err)
	}
	if err := client.WaitReady(ctx, "unknown"); !errors.Is(err, ErrUnknownProcess) {
		t.Errorf("a wait for an unknown process gives %v", err)
	}
	if err := client.Delete(ctx, "unknown"); err != nil {
		t.Errorf("a delete of an unknown process gives %v", err)
	}
	_, err := client.Put(ctx, "app", Spec{Command: "true"})
	if apiError, isAPIError := errors.AsType[*APIError](err); !isAPIError || apiError.StatusCode != http.StatusBadRequest {
		t.Errorf("a specification with no log file gives %v", err)
	}
	if errors.Is(err, ErrDaemonDown) {
		t.Error("an API error looks like a dead daemon")
	}
	err = client.SetRoutes(ctx, []Route{{Host: "a.localhost", Port: 1}, {Host: "A.localhost", Port: 2}})
	if apiError, isAPIError := errors.AsType[*APIError](err); !isAPIError || apiError.StatusCode != http.StatusBadRequest {
		t.Errorf("two routes for one host give %v", err)
	}

	for _, name := range []string{"", ".", ".."} {
		spec := Spec{Command: "true", LogFile: filepath.Join(t.TempDir(), "app.log")}
		if _, err := client.Put(ctx, name, spec); err == nil || !strings.Contains(err.Error(), "name") {
			t.Errorf("the client: a process with the name %q gives %v", name, err)
		}
		if _, err := served.supervisor.Put(name, spec); err == nil || !strings.Contains(err.Error(), "name") {
			t.Errorf("the supervisor: a process with the name %q gives %v", name, err)
		}
	}
	if processes := served.supervisor.List(); len(processes) != 0 {
		t.Errorf("a refused name left %+v", processes)
	}

	exitingSpec := Spec{Command: "exit 9", LogFile: filepath.Join(t.TempDir(), "app.log"), Ready: &Probe{Command: "false"}}
	if _, err := client.Put(ctx, "broken", exitingSpec); err != nil {
		t.Fatal(err)
	}
	err = client.WaitReady(ctx, "broken")
	if exitedError, isExited := errors.AsType[*ExitedError](err); !isExited || exitedError.ExitCode != 9 {
		t.Errorf("a wait for an exited process gives %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.Status(cancelled); !errors.Is(err, context.Canceled) || errors.Is(err, ErrDaemonDown) {
		t.Errorf("a cancelled call gives %v, want only the cancel", err)
	}
}

func TestClientTellsThatTheDaemonIsDown(t *testing.T) {
	dir := shortDir(t)

	t.Run("no socket file", func(t *testing.T) {
		_, err := NewClient(filepath.Join(dir, "missing.sock")).Status(t.Context())
		if !errors.Is(err, ErrDaemonDown) {
			t.Fatalf("got %v, want ErrDaemonDown", err)
		}
	})

	t.Run("socket file of a dead daemon", func(t *testing.T) {
		socketPath := filepath.Join(dir, "stale.sock")
		leaveStaleSocket(t, socketPath)
		_, err := NewClient(socketPath).Status(t.Context())
		if !errors.Is(err, ErrDaemonDown) {
			t.Fatalf("got %v, want ErrDaemonDown", err)
		}
		if _, statErr := os.Stat(socketPath); statErr != nil {
			t.Fatalf("the client removed the socket file: %v", statErr)
		}
	})
}

func leaveStaleSocket(t *testing.T, socketPath string) {
	t.Helper()
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownTellsTheStoppedDaemonFromTheNextOneOnTheSameSocket(t *testing.T) {
	socketPath := filepath.Join(shortDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	const stoppedPID, nextPID = 111, 222
	// One server plays both daemons: the next daemon holds the path before the caller looks at it.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/shutdown", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, http.StatusOK, shutdownReply{PID: stoppedPID})
	})
	mux.HandleFunc("GET /v1/status", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, http.StatusOK, Status{PID: nextPID})
	})
	httpServer := &http.Server{Handler: mux}
	serving := make(chan struct{})
	go func() {
		defer close(serving)
		_ = httpServer.Serve(listener)
	}()
	t.Cleanup(func() {
		httpServer.Close()
		<-serving
	})

	begin := time.Now()
	if err := NewClient(socketPath).Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown gives %v after %s", err, time.Since(begin))
	}
	if took := time.Since(begin); took > shutdownExitWait/2 {
		t.Fatalf("Shutdown took %s although the stopped daemon is gone", took)
	}
}
