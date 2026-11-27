package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func homeWithFile(t *testing.T, relativePath, content string) string {
	t.Helper()
	homeDir := t.TempDir()
	filePath := filepath.Join(homeDir, relativePath)
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return homeDir
}

func TestLoadGlobalWithoutAFileGivesTheDefaults(t *testing.T) {
	global, err := LoadGlobal(t.TempDir())

	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if global.Proxy != (Proxy{Listen: "127.0.0.1:4580", Domain: "localhost"}) || global.Ports != (PortRange{First: 20000, Last: 29999}) || !slices.Equal(global.ShellEnv.Args, []string{"-l"}) || len(global.Workspaces) != 0 {
		t.Errorf("global = %+v, want the defaults", global)
	}
}

func TestLoadGlobalReadsTheProxyThePortsTheShellAndTheWorkspaces(t *testing.T) {
	homeDir := homeWithFile(t, GlobalFilePath, `
proxy:
  listen: 127.0.0.1:4581
  domain: localtest.me
ports:
  first: 30000
  last: 30999
shell_env:
  args: [-l, -i]
workspaces:
  shop:
    repos:
      web: ~/elsewhere/web
    env:
      LOG_LEVEL: debug
    apps:
      front:
        API_TOKEN: secret
`)

	global, err := LoadGlobal(homeDir)

	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if global.Proxy != (Proxy{Listen: "127.0.0.1:4581", Domain: "localtest.me"}) {
		t.Errorf("proxy = %+v", global.Proxy)
	}
	if global.Ports != (PortRange{First: 30000, Last: 30999}) || !global.Ports.Contains(30000) || global.Ports.Contains(31000) {
		t.Errorf("ports = %+v", global.Ports)
	}
	if !slices.Equal(global.ShellEnv.Args, []string{"-l", "-i"}) {
		t.Errorf("shell_env.args = %v", global.ShellEnv.Args)
	}
	shop := global.Workspaces["shop"]
	if shop.Repos["web"] != "~/elsewhere/web" || shop.Env["LOG_LEVEL"] != "debug" || shop.Apps["front"]["API_TOKEN"] != "secret" {
		t.Errorf("workspaces.shop = %+v", shop)
	}
}

func TestLoadGlobalKeepsTheDefaultOfAKeyThatTheFileDoesNotSet(t *testing.T) {
	global, err := LoadGlobal(homeWithFile(t, GlobalFilePath, "proxy:\n  listen: 127.0.0.1:4581\nports:\n  first: 21000\n"))

	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if global.Proxy.Domain != "localhost" || global.Ports != (PortRange{First: 21000, Last: 29999}) || !slices.Equal(global.ShellEnv.Args, []string{"-l"}) {
		t.Errorf("global = %+v, want the default domain, last port and shell flag", global)
	}
}

func TestLoadGlobalOfAnEmptyFileGivesTheDefaults(t *testing.T) {
	global, err := LoadGlobal(homeWithFile(t, GlobalFilePath, ""))
	if err != nil || global.Proxy.Listen != DefaultProxyListen {
		t.Errorf("LoadGlobal = %+v, %v; want the defaults", global, err)
	}
}

func TestLoadGlobalRefusesTheOldPersonalFileAndNamesTheNewFile(t *testing.T) {
	homeDir := homeWithFile(t, ".config/ilot/env.yaml", "env:\n  LOG_LEVEL: debug\n")

	_, err := LoadGlobal(homeDir)

	for _, wantPart := range []string{filepath.Join(homeDir, ".config/ilot/env.yaml"), filepath.Join(homeDir, ".config/ilot/config.yaml"), "workspaces.<name of the workspace>"} {
		if err == nil || !strings.Contains(err.Error(), wantPart) {
			t.Errorf("LoadGlobal = %v, want an error with %q", err, wantPart)
		}
	}
}

func TestLoadGlobalRejectsWrongValuesAndOldKeys(t *testing.T) {
	testCases := []struct {
		yaml            string
		wantErrorSubstr string
	}{
		{yaml: "proxy:\n  listen: localhost\n", wantErrorSubstr: "proxy.listen"},
		{yaml: "proxy:\n  domain: ''\n", wantErrorSubstr: "proxy.domain"},
		{yaml: "ports:\n  first: 30000\n", wantErrorSubstr: "first 30000 and last 29999"},
		{yaml: "ports:\n  last: 70000\n", wantErrorSubstr: "between 1 and 65535"},
		{yaml: "env:\n  LOG_LEVEL: debug\n", wantErrorSubstr: "env: it moved under workspaces.<name of the workspace>.env"},
		{yaml: "apps:\n  front: {A: b}\n", wantErrorSubstr: "apps: it moved under workspaces.<name of the workspace>.apps"},
		{yaml: "repos:\n  web: {path: ~/web}\n", wantErrorSubstr: "repos: it moved under workspaces.<name of the workspace>.repos, as <name>: <path>"},
		{yaml: "prxy: {}\n", wantErrorSubstr: "prxy"},
	}
	for _, testCase := range testCases {
		_, err := LoadGlobal(homeWithFile(t, GlobalFilePath, testCase.yaml))
		if err == nil || !strings.Contains(err.Error(), testCase.wantErrorSubstr) || !strings.Contains(err.Error(), "config.yaml") {
			t.Errorf("LoadGlobal(%q) = %v, want an error with %q that names the file", testCase.yaml, err, testCase.wantErrorSubstr)
		}
	}
}
