package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
)

// healthyMachine gives a doctor whose every check passes, with no workspace to read.
func healthyMachine(t *testing.T) doctor {
	t.Helper()
	homeDir := t.TempDir()
	return doctor{
		HomeDir:         homeDir,
		StateDir:        filepath.Join(homeDir, ".local", "state", "ilot"),
		IlotVersion:     "v0.1.0",
		Global:          config.DefaultGlobal(),
		GlobalErr:       nil,
		WorkspaceConfig: func() (config.Config, error) { return config.Config{}, errors.New("no ilot.yaml") },
		LookPath:        func(program, _ string) (string, error) { return "/usr/bin/" + program, nil },
		RunVersion: func(_ context.Context, program string, _ ...string) (string, error) {
			return filepath.Base(program) + " 1.0", nil
		},
		LookupHost:   func(context.Context, string) ([]string, error) { return []string{"127.0.0.1", "::1"}, nil },
		DaemonStatus: func(context.Context) (daemon.Status, error) { return daemon.Status{}, daemon.ErrDaemonDown },
		ShellEnv:     func(context.Context) (map[string]string, error) { return map[string]string{"PATH": "/usr/bin"}, nil },
		ListenOn:     func(string) error { return nil },
	}
}

func checkNamed(t *testing.T, checks []doctorCheck, name string) []doctorCheck {
	t.Helper()
	var named []doctorCheck
	for _, check := range checks {
		if check.Name == name {
			named = append(named, check)
		}
	}
	if len(named) == 0 {
		t.Fatalf("no check %q in %+v", name, checks)
	}
	return named
}

func TestDoctorOnAHealthyMachineNeedsNoFix(t *testing.T) {
	for _, check := range healthyMachine(t).run(t.Context()) {
		if check.Status == checkFix {
			t.Errorf("check %+v, want no fix", check)
		}
	}
}

func TestDoctorReportsTheStateOfTheCompanyVersionWithTheCommandThatClearsIt(t *testing.T) {
	machine := healthyMachine(t)
	for _, oldDir := range []string{"stacks/master", "stacks/devbox", "process-compose"} {
		if err := os.MkdirAll(filepath.Join(machine.StateDir, oldDir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	oldPersonalFile := filepath.Join(machine.HomeDir, ".config", "ilot", "env.yaml")
	if err := os.MkdirAll(filepath.Dir(oldPersonalFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPersonalFile, []byte("env: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldState := checkNamed(t, machine.run(t.Context()), "old state")

	wants := []string{
		"(devbox, master)",
		"rm -rf " + filepath.Join(machine.StateDir, "stacks"),
		"rm -rf " + filepath.Join(machine.StateDir, "process-compose"),
		"rm " + oldPersonalFile,
	}
	for _, want := range wants {
		if !strings.Contains(detailsOf(oldState), want) {
			t.Errorf("old state = %+v, want %q", oldState, want)
		}
	}
	for _, check := range oldState {
		if check.Status != checkFix {
			t.Errorf("check %+v, want a fix", check)
		}
	}
}

func detailsOf(checks []doctorCheck) string {
	var details []string
	for _, check := range checks {
		details = append(details, check.Detail)
	}
	return strings.Join(details, "\n")
}

func TestDoctorNamesTheFixesOfANameThatDoesNotResolve(t *testing.T) {
	machine := healthyMachine(t)
	machine.LookupHost = func(context.Context, string) ([]string, error) { return nil, errors.New("no such host") }

	lookup := checkNamed(t, machine.run(t.Context()), "name lookup")[0]

	if lookup.Status != checkFix || !strings.Contains(lookup.Detail, "ilot-doctor.localhost") || !strings.Contains(lookup.Detail, "localtest.me") || !strings.Contains(lookup.Detail, "port template") {
		t.Errorf("name lookup = %+v, want a fix that names the host, another domain and the port template", lookup)
	}
}

func TestDoctorFindsADaemonOfAnotherVersionAndAProxyThatDoesNotListen(t *testing.T) {
	machine := healthyMachine(t)
	machine.DaemonStatus = func(context.Context) (daemon.Status, error) {
		return daemon.Status{Version: "v0.0.9", PID: 7, ProxyError: "the proxy cannot listen on 127.0.0.1:4580: address already in use"}, nil
	}

	checks := machine.run(t.Context())

	if daemonCheck := checkNamed(t, checks, "daemon")[0]; daemonCheck.Status != checkFix || !strings.Contains(daemonCheck.Detail, "ilot daemon restart") {
		t.Errorf("daemon = %+v, want a fix with ilot daemon restart", daemonCheck)
	}
	if proxyCheck := checkNamed(t, checks, "proxy")[0]; proxyCheck.Status != checkFix || !strings.Contains(proxyCheck.Detail, "address already in use") {
		t.Errorf("proxy = %+v, want the error of the proxy", proxyCheck)
	}
}

func TestDoctorFindsAProxyAddressThatAnotherProgramHolds(t *testing.T) {
	machine := healthyMachine(t)
	machine.ListenOn = func(string) error { return errors.New("address already in use") }

	if proxyCheck := checkNamed(t, machine.run(t.Context()), "proxy")[0]; proxyCheck.Status != checkFix || !strings.Contains(proxyCheck.Detail, "proxy.listen") {
		t.Errorf("proxy = %+v, want a fix that names proxy.listen", proxyCheck)
	}
}

func TestDoctorTestsWatchexecAndDockerOnlyForTheAppsThatNeedThem(t *testing.T) {
	machine := healthyMachine(t)
	machine.WorkspaceConfig = func() (config.Config, error) {
		return config.Config{Apps: map[string]config.App{"api": {Run: "api", Watch: config.Watch{Paths: []string{"."}}}}}, nil
	}
	var searchedPaths []string
	machine.LookPath = func(program, pathList string) (string, error) {
		searchedPaths = append(searchedPaths, pathList)
		return "", errors.New(program + " is not in the PATH of the apps")
	}

	checks := machine.run(t.Context())

	if watchexec := checkNamed(t, checks, "watchexec")[0]; watchexec.Status != checkFix || !strings.Contains(watchexec.Detail, "api") {
		t.Errorf("watchexec = %+v, want a fix that names the app with watch", watchexec)
	}
	if docker := checkNamed(t, checks, "docker")[0]; docker.Status != checkSkip {
		t.Errorf("docker = %+v, want it skipped: no app is a container", docker)
	}
	if len(searchedPaths) != 1 || searchedPaths[0] != "/usr/bin" {
		t.Errorf("searched %v, want the PATH of the login shell", searchedPaths)
	}
}

func TestDoctorReportsAShellThatGivesNoEnvironment(t *testing.T) {
	machine := healthyMachine(t)
	machine.ShellEnv = func(context.Context) (map[string]string, error) { return nil, errors.New("the shell exited with 1") }

	if shell := checkNamed(t, machine.run(t.Context()), "shell")[0]; shell.Status != checkFix || !strings.Contains(shell.Detail, "shell_env.args") {
		t.Errorf("shell = %+v, want a fix that names shell_env.args", shell)
	}
}

func TestDoctorReportsAMissingGit(t *testing.T) {
	machine := healthyMachine(t)
	machine.RunVersion = func(context.Context, string, ...string) (string, error) {
		return "", errors.New("executable file not found")
	}

	if git := checkNamed(t, machine.run(t.Context()), "git")[0]; git.Status != checkFix {
		t.Errorf("git = %+v, want a fix", git)
	}
}
