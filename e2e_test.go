//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
)

const e2eWait = 30 * time.Second

// e2eMachine is a machine of one user for the test: a home folder, a workspace in it, the ilot binary and a free proxy address.
type e2eMachine struct {
	t            *testing.T
	homeDir      string
	ilotBinary   string
	proxyAddress string
	environ      []string
}

func newE2EMachine(t *testing.T) *e2eMachine {
	t.Helper()
	// Short, so that the socket of the daemon fits in the state folder on macOS too.
	homeDir, err := os.MkdirTemp("/tmp", "ilot-e2e")
	if err != nil {
		t.Fatal(err)
	}
	homeDir, err = filepath.EvalSymlinks(homeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(homeDir) })

	ilotBinary := filepath.Join(homeDir, "bin", "ilot")
	if output, err := exec.Command("go", "build", "-o", ilotBinary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build ilot: %v\n%s", err, output)
	}

	machine := &e2eMachine{t: t, homeDir: homeDir, ilotBinary: ilotBinary, proxyAddress: freeLoopbackAddress(t)}
	machine.environ = []string{
		"HOME=" + homeDir,
		"SHELL=/bin/sh",
		"PATH=" + os.Getenv("PATH"),
	}
	machine.writeProfile()
	machine.writeGlobalFile()
	t.Cleanup(machine.stopDaemon)
	return machine
}

func freeLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

// The apps get the environment of a login shell of this home folder, so the profile is what gives them the Go toolchain.
func (machine *e2eMachine) writeProfile() {
	machine.t.Helper()
	goEnv := func(name string) string {
		output, err := exec.Command("go", "env", name).Output()
		if err != nil {
			machine.t.Fatalf("go env %s: %v", name, err)
		}
		return strings.TrimSpace(string(output))
	}
	profile := fmt.Sprintf("export PATH='%s'\nexport GOCACHE='%s'\nexport GOMODCACHE='%s'\nexport GOTOOLCHAIN=local\n",
		filepath.Join(goEnv("GOROOT"), "bin")+":"+os.Getenv("PATH"), goEnv("GOCACHE"), goEnv("GOMODCACHE"))
	if err := os.WriteFile(filepath.Join(machine.homeDir, ".profile"), []byte(profile), 0o600); err != nil {
		machine.t.Fatal(err)
	}
}

// The global file moves the proxy to a free port, so the test runs beside the ilot of the developer.
func (machine *e2eMachine) writeGlobalFile() {
	machine.t.Helper()
	globalFile := filepath.Join(machine.homeDir, config.GlobalFilePath)
	if err := os.MkdirAll(filepath.Dir(globalFile), 0o700); err != nil {
		machine.t.Fatal(err)
	}
	if err := os.WriteFile(globalFile, []byte("proxy:\n  listen: "+machine.proxyAddress+"\n"), 0o600); err != nil {
		machine.t.Fatal(err)
	}
}

func (machine *e2eMachine) daemonPaths() daemon.Paths {
	return daemon.Paths{StateDir: filepath.Join(machine.homeDir, ".local", "state", "ilot")}
}

func (machine *e2eMachine) daemonClient() *daemon.Client {
	return daemon.NewClient(machine.daemonPaths().Socket())
}

func (machine *e2eMachine) run(dir, program string, arguments ...string) string {
	machine.t.Helper()
	command := exec.Command(program, arguments...)
	command.Dir = dir
	command.Env = machine.environ
	output, err := command.CombinedOutput()
	if err != nil {
		machine.t.Fatalf("%s %s in %s: %v\n%s\n%s", filepath.Base(program), strings.Join(arguments, " "), dir, err, output, machine.daemonLog())
	}
	return string(output)
}

func (machine *e2eMachine) ilot(dir string, arguments ...string) string {
	machine.t.Helper()
	return machine.run(dir, machine.ilotBinary, arguments...)
}

func (machine *e2eMachine) git(dir string, arguments ...string) {
	machine.t.Helper()
	machine.run(dir, "git", append([]string{"-c", "user.name=ilot", "-c", "user.email=ilot@example.com"}, arguments...)...)
}

func (machine *e2eMachine) daemonLog() string {
	content, _ := os.ReadFile(machine.daemonPaths().LogFile())
	return "daemon.log:\n" + string(content)
}

func (machine *e2eMachine) copySampleWorkspace(sampleDir string) (mainCheckout string) {
	machine.t.Helper()
	mainCheckout = filepath.Join(machine.homeDir, "src", "shop")
	if err := os.CopyFS(mainCheckout, os.DirFS(sampleDir)); err != nil {
		machine.t.Fatal(err)
	}
	machine.git(mainCheckout, "init", "--quiet", "--initial-branch=main")
	machine.git(mainCheckout, "add", "--all")
	machine.git(mainCheckout, "commit", "--quiet", "--message", "sample workspace")
	return mainCheckout
}

// get asks the proxy for a host, with no name lookup: not every Linux machine resolves the names under localhost.
func (machine *e2eMachine) get(host string) (status int, body string, err error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, network, machine.proxyAddress)
			},
		},
	}
	_, proxyPort, _ := net.SplitHostPort(machine.proxyAddress)
	response, err := client.Get("http://" + host + ":" + proxyPort + "/")
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	return response.StatusCode, string(content), err
}

func (machine *e2eMachine) wantBody(host, wantBody string) {
	machine.t.Helper()
	status, body, err := machine.get(host)
	if err != nil || status != http.StatusOK || body != wantBody {
		machine.t.Fatalf("GET %s through the proxy = %d %q, %v; want 200 %q\n%s", host, status, body, err, wantBody, machine.daemonLog())
	}
}

func (machine *e2eMachine) stopDaemon() {
	ctx, cancel := context.WithTimeout(context.Background(), e2eWait)
	defer cancel()
	status, err := machine.daemonClient().Status(ctx)
	if err != nil {
		return
	}
	if err := machine.daemonClient().Shutdown(ctx); err != nil {
		_ = syscall.Kill(status.PID, syscall.SIGKILL)
		machine.t.Errorf("the daemon %d did not shut down and was killed: %v", status.PID, err)
	}
}

func isAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func waitUntilGone(t *testing.T, what string, pids ...int) {
	t.Helper()
	// kill(0) reaches the process group of the test, which is always alive.
	if slices.Contains(pids, 0) {
		t.Fatalf("%s: no process ID in %v", what, pids)
	}
	deadline := time.Now().Add(e2eWait)
	for {
		var alive []int
		for _, pid := range pids {
			if isAlive(pid) {
				alive = append(alive, pid)
			}
		}
		if len(alive) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the processes %v still run after %s", what, alive, e2eWait)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (machine *e2eMachine) appPIDs() map[string]int {
	machine.t.Helper()
	processes, err := machine.daemonClient().Processes(machine.t.Context())
	if err != nil {
		machine.t.Fatalf("list the processes of the daemon: %v", err)
	}
	pidByName := map[string]int{}
	for _, process := range processes {
		pidByName[process.Name] = process.PID
	}
	return pidByName
}

func (machine *e2eMachine) statusByApp(dirInWorkspace string) map[string]string {
	machine.t.Helper()
	command := exec.Command(machine.ilotBinary, "ls", "--json")
	command.Dir = dirInWorkspace
	command.Env = machine.environ
	output, err := command.Output()
	if err != nil {
		machine.t.Fatalf("ilot ls --json: %v", err)
	}
	var listing lsJSONOutput
	if err := json.Unmarshal(output, &listing); err != nil {
		machine.t.Fatalf("ilot ls --json printed %q: %v", output, err)
	}
	statusByApp := map[string]string{}
	for _, row := range listing.Apps {
		statusByApp[row.App+"."+row.Stack] = row.Status
	}
	return statusByApp
}

func TestEndToEndAnAppRunsInBaseAndInAWorktreeBehindTheProxy(t *testing.T) {
	machine := newE2EMachine(t)
	mainCheckout := machine.copySampleWorkspace(filepath.Join("testdata", "e2e", "shop"))

	startOutput := machine.ilot(mainCheckout, "start", "hello")
	_, proxyPort, _ := net.SplitHostPort(machine.proxyAddress)
	if wantURL := "http://hello.base.shop.localhost:" + proxyPort; !strings.Contains(startOutput, wantURL) {
		t.Errorf("ilot start printed %q, want the URL %s", startOutput, wantURL)
	}
	machine.wantBody("hello.base.shop.localhost", "hello from base\n")
	if auditLog, err := os.ReadFile(filepath.Join(machine.daemonPaths().StateDir, "ilot.log")); err != nil || !strings.Contains(string(auditLog), `"msg":"port given"`) || !strings.Contains(string(auditLog), `"cmd":"start <apps>"`) {
		t.Errorf("ilot.log = %q, %v; want the records of the start", auditLog, err)
	}

	worktree := filepath.Join(machine.homeDir, "src", "shop-retry")
	machine.git(mainCheckout, "worktree", "add", "--quiet", worktree, "-b", "PROJ-1-retry-jobs")
	machine.ilot(worktree, "start", "hello")
	machine.wantBody("hello.proj-1.shop.localhost", "hello from proj-1\n")
	machine.wantBody("hello.base.shop.localhost", "hello from base\n")

	if status, body, err := machine.get("unknown.localhost"); err != nil || status != http.StatusNotFound || !strings.Contains(body, "hello.proj-1.shop.localhost") {
		t.Errorf("GET of an unknown host = %d %q, %v; want the 404 page that lists the routes", status, body, err)
	}

	// A killed daemon leaves its apps behind. The next daemon kills them, and ilot start --all restores the apps.
	killedDaemon, err := machine.daemonClient().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pidsBeforeTheKill := machine.appPIDs()
	// No daemon stops these apps if the test fails before the next daemon kills them.
	t.Cleanup(func() {
		for _, pid := range pidsBeforeTheKill {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
	if err := syscall.Kill(killedDaemon.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitUntilGone(t, "the killed daemon", killedDaemon.PID)
	if _, _, err := machine.get("hello.base.shop.localhost"); err == nil {
		t.Fatal("the proxy still answers after the daemon was killed")
	}

	// The first start bound the worktree to proj-1, so another branch does not move it to another stack.
	machine.git(worktree, "switch", "--quiet", "--create", "another-idea")
	if stackOutput := machine.ilot(worktree, "stack", "show"); !strings.Contains(stackOutput, "stack: proj-1 (this worktree is bound to it)") {
		t.Errorf("ilot stack show after git switch printed %q, want the stack proj-1", stackOutput)
	}
	if statusByApp := machine.statusByApp(worktree); statusByApp["hello.base"] != "down" || statusByApp["hello.proj-1"] != "down" {
		t.Errorf("ilot ls after the kill = %v, want both apps down", statusByApp)
	}
	if listOutput := machine.ilot(worktree, "ls"); !strings.Contains(listOutput, "ilot start --all starts them again") {
		t.Errorf("ilot ls after the kill printed %q, want the line that restores the apps", listOutput)
	}
	machine.ilot(worktree, "start", "--all")

	waitUntilGone(t, "the apps of the killed daemon", pidsBeforeTheKill["hello.base.shop"], pidsBeforeTheKill["hello.proj-1.shop"])
	machine.wantBody("hello.base.shop.localhost", "hello from base\n")
	machine.wantBody("hello.proj-1.shop.localhost", "hello from proj-1\n")
	if statusByApp := machine.statusByApp(worktree); statusByApp["hello.base"] != "ready" || statusByApp["hello.proj-1"] != "ready" {
		t.Errorf("ilot ls = %v, want both apps ready", statusByApp)
	}

	pidByName := machine.appPIDs()
	machine.ilot(worktree, "stop", "hello")
	waitUntilGone(t, "the stopped app", pidByName["hello.proj-1.shop"])
	if status, body, err := machine.get("hello.proj-1.shop.localhost"); err != nil || status != http.StatusServiceUnavailable || !strings.Contains(body, "ilot start hello") {
		t.Errorf("GET of the stopped app = %d %q, %v; want the 503 page with the command that starts it", status, body, err)
	}
	machine.wantBody("hello.base.shop.localhost", "hello from base\n")

	daemonStatus, err := machine.daemonClient().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	machine.stopDaemon()
	waitUntilGone(t, "the daemon and its last app", daemonStatus.PID, pidByName["hello.base.shop"])
}

// CI on Linux has Docker, so a Docker that does not work there is a failure. On another machine, the test is skipped.
func requireDocker(t *testing.T) {
	t.Helper()
	mustHaveDocker := os.Getenv("CI") != "" && runtime.GOOS == "linux"
	skipOrFail := t.Skipf
	if mustHaveDocker {
		skipOrFail = t.Fatalf
	}
	if _, err := exec.LookPath("docker"); err != nil {
		skipOrFail("docker is not on PATH: %v", err)
	}
	if output, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		skipOrFail("docker does not answer: %v\n%s", err, output)
	}
}

func TestEndToEndAContainerAppAnswersBehindTheProxy(t *testing.T) {
	requireDocker(t)
	machine := newE2EMachine(t)
	mainCheckout := machine.copySampleWorkspace(filepath.Join("testdata", "e2e", "shop"))
	const containerName = "ilot_web_base_shop"
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", containerName).Run() })

	machine.ilot(mainCheckout, "start", "--timeout", "5m", "web")

	if status, body, err := machine.get("web.base.shop.localhost"); err != nil || status != http.StatusOK || !strings.Contains(body, "nginx") {
		t.Fatalf("GET of the container through the proxy = %d %q, %v; want the page of nginx\n%s", status, body, err, machine.daemonLog())
	}
	published, err := exec.Command("docker", "port", containerName, "80/tcp").Output()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(published)), "127.0.0.1:") {
		t.Errorf("docker port = %q, %v; want the port published on the loopback address only", published, err)
	}

	machine.ilot(mainCheckout, "stop", "web")
	deadline := time.Now().Add(e2eWait)
	for {
		remaining, err := exec.Command("docker", "ps", "--all", "--quiet", "--filter", "name=^"+containerName+"$").Output()
		if err == nil && strings.TrimSpace(string(remaining)) == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the container %s is still there after ilot stop: %q, %v", containerName, remaining, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
