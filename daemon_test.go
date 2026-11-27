package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
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
