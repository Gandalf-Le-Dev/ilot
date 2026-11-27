package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

var quickProbes = SupervisorOptions{ProbeInterval: 20 * time.Millisecond, AliveWindow: 50 * time.Millisecond}

func sleeperWithProbe(t *testing.T, probe Probe) Spec {
	t.Helper()
	workingDir := t.TempDir()
	return Spec{
		Command: "sleep 300",
		Dir:     workingDir,
		Env:     map[string]string{"PATH": "/usr/bin:/bin"},
		LogFile: filepath.Join(workingDir, "app.log"),
		Ready:   &probe,
	}
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func staysInState(t *testing.T, supervisor *Supervisor, name string, state State, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if got := processNamed(t, supervisor, name).State; got != state {
			t.Fatalf("%s is %s, want %s", name, got, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTCPProbe(t *testing.T) {
	supervisor := newTestSupervisor(t, quickProbes)
	port := freeLoopbackPort(t)

	started, err := supervisor.Put("app", sleeperWithProbe(t, Probe{Port: port}))
	if err != nil {
		t.Fatal(err)
	}
	if started.State != StateStarting {
		t.Fatalf("a process with a probe starts as %s", started.State)
	}
	staysInState(t, supervisor, "app", StateStarting, 100*time.Millisecond)

	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	waitForState(t, supervisor, "app", StateReady)
	if err := supervisor.WaitReady(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPProbe(t *testing.T) {
	var isHealthy atomic.Bool
	var requestedPath atomic.Value
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestedPath.Store(request.URL.Path)
		if !isHealthy.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer backend.Close()
	address := backend.Listener.Addr().(*net.TCPAddr)
	supervisor := newTestSupervisor(t, quickProbes)

	if _, err := supervisor.Put("app", sleeperWithProbe(t, Probe{Host: address.IP.String(), Port: address.Port, Path: "/health"})); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the first probe", func() bool { return requestedPath.Load() != nil })
	staysInState(t, supervisor, "app", StateStarting, 100*time.Millisecond)

	isHealthy.Store(true)
	waitForState(t, supervisor, "app", StateReady)
	if requestedPath.Load() != "/health" {
		t.Errorf("the probe asked for %v", requestedPath.Load())
	}
}

func TestCommandProbeRunsInTheFolderAndTheEnvironmentOfTheProcess(t *testing.T) {
	supervisor := newTestSupervisor(t, quickProbes)
	spec := sleeperWithProbe(t, Probe{Command: `test -f "$FLAG"`})
	spec.Env["FLAG"] = "ready.flag"

	if _, err := supervisor.Put("app", spec); err != nil {
		t.Fatal(err)
	}
	staysInState(t, supervisor, "app", StateStarting, 100*time.Millisecond)

	if err := os.WriteFile(filepath.Join(spec.Dir, "ready.flag"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitForState(t, supervisor, "app", StateReady)
}

func TestAProcessThatIsNotReadyAfterTheWaitStillRuns(t *testing.T) {
	supervisor := newTestSupervisor(t, quickProbes)
	spec := sleeperWithProbe(t, Probe{Command: "echo probe >> probes; false"})

	started, err := supervisor.Put("slow", spec)
	if err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	if err := supervisor.WaitReady(wait, "slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the wait gives %v, want the deadline", err)
	}

	probes, _ := os.ReadFile(filepath.Join(spec.Dir, "probes"))
	if count := strings.Count(string(probes), "probe"); count < 3 {
		t.Fatalf("the probe ran %d times, want several", count)
	}
	after := processNamed(t, supervisor, "slow")
	if after.State != StateStarting || after.PID != started.PID || !isProcessAlive(started.PID) {
		t.Fatalf("after the wait the process is %+v, want the same process, still starting", after)
	}
}

func TestWaitReadyReportsAProcessThatExited(t *testing.T) {
	supervisor := newTestSupervisor(t, quickProbes)
	spec := sleeperWithProbe(t, Probe{Command: "false"})
	spec.Command = "exit 4"

	if _, err := supervisor.Put("broken", spec); err != nil {
		t.Fatal(err)
	}
	err := supervisor.WaitReady(t.Context(), "broken")
	exitedError, isExited := errors.AsType[*ExitedError](err)
	if !isExited || exitedError.ExitCode != 4 {
		t.Fatalf("got %v, want an exit with code 4", err)
	}
	if got := processNamed(t, supervisor, "broken").State; got != StateExited {
		t.Fatalf("the process is %s", got)
	}
}

func TestAProcessWithNoProbeThatExitsAtOnceIsNeverReady(t *testing.T) {
	supervisor := newTestSupervisor(t, SupervisorOptions{})
	spec := sleeperWithProbe(t, Probe{})
	spec.Ready = nil
	spec.Command = "exit 4"

	started, err := supervisor.Put("broken", spec)
	if err != nil {
		t.Fatal(err)
	}
	if started.State != StateStarting {
		t.Fatalf("a process is %s at its start, before it proved that it stays alive", started.State)
	}
	err = supervisor.WaitReady(t.Context(), "broken")
	if exitedError, isExited := errors.AsType[*ExitedError](err); !isExited || exitedError.ExitCode != 4 {
		t.Fatalf("got %v, want an exit with code 4", err)
	}
}

func TestAProcessThatExitedIsNotReadyWhileAGrandchildHoldsItsOutput(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl makes the grandchild that leaves the group")
	}
	supervisor := newTestSupervisor(t, SupervisorOptions{})
	// The window ends right after the shell exits, so it ends during the drain however slow perl is to start.
	windowEnd := make(chan time.Time)
	supervisor.afterAliveWindow = func(time.Duration) <-chan time.Time { return windowEnd }
	spec := sleeperWithProbe(t, Probe{})
	spec.Ready = nil
	spec.Command = `perl -MPOSIX -e 'setsid(); open(my $mark, ">", "left") or die; close($mark); sleep 300' &
echo $! > grandchild.pid
while [ ! -e left ]; do sleep 0.02; done
exit 1`

	if _, err := supervisor.Put("broken", spec); err != nil {
		t.Fatal(err)
	}
	grandchild := readPIDFile(t, filepath.Join(spec.Dir, "grandchild.pid"))
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })
	supervisor.mu.Lock()
	dead := supervisor.entries["broken"].run.dead
	supervisor.mu.Unlock()
	<-dead.Done()
	close(windowEnd)

	begin := time.Now()
	for time.Since(begin) < outputDrainTimeout {
		if got := processNamed(t, supervisor, "broken").State; got == StateReady {
			t.Fatalf("the process is ready %s after its shell exited", time.Since(begin))
		}
		time.Sleep(10 * time.Millisecond)
	}
	err := supervisor.WaitReady(t.Context(), "broken")
	if exitedError, isExited := errors.AsType[*ExitedError](err); !isExited || exitedError.ExitCode != 1 {
		t.Fatalf("got %v, want an exit with code 1", err)
	}
	if !isProcessAlive(grandchild) {
		t.Fatal("the grandchild did not leave the group, so nothing held the output")
	}
}

func TestWaitReadyReportsAProcessThatExitedAfterItWasReady(t *testing.T) {
	supervisor := newTestSupervisor(t, quickProbes)
	spec := sleeperWithProbe(t, Probe{})
	spec.Ready = nil
	spec.Command = "sleep 0.3; exit 5"

	if _, err := supervisor.Put("short", spec); err != nil {
		t.Fatal(err)
	}
	waitForState(t, supervisor, "short", StateReady)
	waitForState(t, supervisor, "short", StateExited)
	// Many times, because a wait that picks at random between ready and exited is right half of the time.
	for range 20 {
		err := supervisor.WaitReady(t.Context(), "short")
		if exitedError, isExited := errors.AsType[*ExitedError](err); !isExited || exitedError.ExitCode != 5 {
			t.Fatalf("got %v, want an exit with code 5", err)
		}
	}
}

func TestWaitReadyWithNoProbeAndWithAnUnknownName(t *testing.T) {
	supervisor := newTestSupervisor(t, quickProbes)
	spec := sleeperWithProbe(t, Probe{})
	spec.Ready = nil

	if _, err := supervisor.Put("app", spec); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.WaitReady(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.WaitReady(t.Context(), "unknown"); !errors.Is(err, ErrUnknownProcess) {
		t.Fatalf("got %v, want ErrUnknownProcess", err)
	}
}

func TestAProbeWithNoTargetIsRefused(t *testing.T) {
	supervisor := newTestSupervisor(t, quickProbes)
	if _, err := supervisor.Put("app", sleeperWithProbe(t, Probe{Path: "/health"})); err == nil {
		t.Fatal("a probe with a path and no port is accepted")
	}
}

func TestDialLoopbackReachesAServerOnEitherAddress(t *testing.T) {
	for _, address := range []string{"[::1]:0", "127.0.0.1:0"} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Logf("this machine cannot listen on %s: %v", address, err)
			continue
		}
		defer listener.Close()
		_, port, _ := net.SplitHostPort(listener.Addr().String())

		connection, err := dialLoopback(t.Context(), "tcp", net.JoinHostPort("localhost", port))
		if err != nil {
			t.Errorf("a server on %s is not reached: %v", listener.Addr(), err)
			continue
		}
		if got := connection.RemoteAddr().String(); got != listener.Addr().String() {
			t.Errorf("the connection goes to %s, want %s", got, listener.Addr())
		}
		connection.Close()
	}

	_, err := dialLoopback(t.Context(), "tcp", net.JoinHostPort("localhost", strconv.Itoa(freeLoopbackPort(t))))
	if opError, isOpError := errors.AsType[*net.OpError](err); !isOpError || opError.Op != "dial" {
		t.Errorf("a port with no server gives %v, want a dial error that the proxy can tell", err)
	}
}
