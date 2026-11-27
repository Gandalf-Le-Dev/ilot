package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

func parse(t *testing.T, arguments ...string) (*kong.Context, error) {
	t.Helper()
	parser, err := kong.New(&cli{}, kong.Vars{"version": "test"})
	if err != nil {
		t.Fatal(err)
	}
	return parser.Parse(arguments)
}

func TestTheDaemonHasAStatusAStopAndARestartCommand(t *testing.T) {
	for _, subcommand := range []string{"status", "stop", "restart"} {
		parsed, err := parse(t, "daemon", subcommand)
		if err != nil || parsed.Command() != "daemon "+subcommand {
			t.Errorf("parse daemon %s = %v, %v", subcommand, parsed, err)
		}
	}
}

func TestBootAndARestartWithNoAppAreGone(t *testing.T) {
	for _, arguments := range [][]string{{"boot"}, {"restart"}} {
		if _, err := parse(t, arguments...); err == nil {
			t.Errorf("parse %v = nil, want an error", arguments)
		}
	}
}

func TestTheAuditLogNamesTheCommandWithoutArgumentPlaceholders(t *testing.T) {
	for arguments, want := range map[string]string{"start api front": "start", "logs -f api": "logs", "daemon restart": "daemon restart", "stack bind shop-12": "stack bind"} {
		parsed, err := parse(t, strings.Fields(arguments)...)
		if err != nil {
			t.Fatalf("parse %s: %v", arguments, err)
		}
		if got := commandPath(parsed); got != want {
			t.Errorf("commandPath(%s) = %q, want %q", arguments, got, want)
		}
	}
}

func TestAStartWithNoAppParses(t *testing.T) {
	if parsed, err := parse(t, "start"); err != nil || parsed.Command() != "start" {
		t.Errorf("parse start = %v, %v", parsed, err)
	}
}

func TestDaemonStatusPrintsWhyTheProxyDoesNotListen(t *testing.T) {
	startedAt := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	status := daemon.Status{Version: "v0.1.0", PID: 42, StartedAt: startedAt, ProxyError: "the proxy cannot listen on 127.0.0.1:4580: address already in use"}
	var output bytes.Buffer

	printDaemonStatus(&output, status, 3, startedAt.Add(time.Hour))

	for _, wantLine := range []string{"version: v0.1.0\n", "pid: 42\n", "(1h0m0s ago)\n", "proxy: does not listen: the proxy cannot listen on 127.0.0.1:4580: address already in use\n", "processes: 3\n"} {
		if !strings.Contains(output.String(), wantLine) {
			t.Errorf("status = %q, want %q", output.String(), wantLine)
		}
	}
}

func TestDaemonStatusPrintsTheProxyAddress(t *testing.T) {
	var output bytes.Buffer

	printDaemonStatus(&output, daemon.Status{Version: "v0.1.0", ProxyAddress: "127.0.0.1:4580", ShuttingDown: true}, 0, time.Now())

	if !strings.Contains(output.String(), "proxy: 127.0.0.1:4580\n") || !strings.Contains(output.String(), "state: shutting down\n") {
		t.Errorf("status = %q, want the proxy address and the shutdown", output.String())
	}
}

type fakeRestarter struct {
	isUp  bool
	calls []string
}

func (restarter *fakeRestarter) IsUp(context.Context) bool { return restarter.isUp }

func (restarter *fakeRestarter) Shutdown(context.Context) error {
	restarter.calls = append(restarter.calls, "shutdown")
	restarter.isUp = false
	return nil
}

func (restarter *fakeRestarter) Up(context.Context) error {
	restarter.calls = append(restarter.calls, "up")
	restarter.isUp = true
	return nil
}

func TestADaemonRestartStartsTheAppsOfEveryWorkspaceAndNamesTheOnesThatStayDown(t *testing.T) {
	restarter := &fakeRestarter{isUp: true}
	var output bytes.Buffer
	startAllOf := func(_ context.Context, workspaceName string) error {
		restarter.calls = append(restarter.calls, "start "+workspaceName)
		if workspaceName == "blog" {
			return errors.New("parse ilot.yaml: line 3")
		}
		return nil
	}

	err := restartDaemon(t.Context(), &output, restarter, []string{"blog", "shop"}, startAllOf)

	if want := []string{"shutdown", "up", "start blog", "start shop"}; !slices.Equal(restarter.calls, want) {
		t.Errorf("calls = %v, want %v: a broken workspace must not keep the others down", restarter.calls, want)
	}
	if err == nil || !strings.Contains(err.Error(), "the apps of the workspace blog stay down") || !strings.Contains(err.Error(), "ilot start --all --workspace blog") || strings.Contains(err.Error(), "shop") {
		t.Errorf("err = %v, want blog named with the command that starts it", err)
	}
	if !strings.Contains(output.String(), "stopping every app of every stack") || !strings.Contains(output.String(), "workspace shop again") {
		t.Errorf("output = %q, want the stop and each workspace", output.String())
	}
}

func TestADaemonRestartOfADownDaemonAnnouncesNoStop(t *testing.T) {
	var output bytes.Buffer

	if err := restartDaemon(t.Context(), &output, &fakeRestarter{isUp: false}, nil, nil); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(output.String(), "stopping every app") || !strings.Contains(output.String(), "started none") {
		t.Errorf("output = %q, want no stop and a word that no app started", output.String())
	}
}

func TestWorkspacesWithMembersSkipsAWorkspaceWithNoRootOrNoWantedApp(t *testing.T) {
	current := state.New()
	for _, name := range []string{"shop", "blog", "wiki"} {
		if err := current.Claim(name, "/code/"+name); err != nil {
			t.Fatal(err)
		}
	}
	current.Stack("shop", "shop-12").Apps["api"] = state.App{Wanted: true, Ports: nil, Route: nil, Container: ""}
	current.Stack("blog", "base").Apps["front"] = state.App{Wanted: false, Ports: nil, Route: nil, Container: ""}
	current.Stack("orphan", "base").Apps["api"] = state.App{Wanted: true, Ports: nil, Route: nil, Container: ""}

	if got, want := workspacesWithMembers(current), []string{"shop"}; !slices.Equal(got, want) {
		t.Errorf("workspacesWithMembers = %v, want %v", got, want)
	}
}
