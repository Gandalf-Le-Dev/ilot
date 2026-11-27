package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/engine"
	"github.com/Gandalf-Le-Dev/ilot/internal/shellenv"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

type doctorCommand struct {
	Selection workspaceSelection `embed:""`
}

const (
	checkOK   = "ok"
	checkFix  = "fix"
	checkSkip = "skip"
)

type doctorCheck struct {
	Status string
	Name   string
	Detail string
}

// doctor holds what the checks read from the machine, so that a test can give each one.
type doctor struct {
	HomeDir     string
	StateDir    string
	IlotVersion string
	Global      config.Global
	GlobalErr   error
	// The configuration of the base stack of the workspace of the command, or why there is none.
	WorkspaceConfig func() (config.Config, error)
	// LookPath finds a program in a PATH value: the apps run with the PATH of the login shell, not the one of this terminal.
	LookPath     func(program, pathList string) (string, error)
	RunVersion   func(ctx context.Context, program string, arguments ...string) (string, error)
	LookupHost   func(ctx context.Context, host string) ([]string, error)
	DaemonStatus func(ctx context.Context) (daemon.Status, error)
	ShellEnv     func(ctx context.Context) (map[string]string, error)
	// ListenOn tests that an address is free, for a proxy that no daemon holds.
	ListenOn func(address string) error
}

func (command *doctorCommand) Run(ctx context.Context) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	global, globalErr := config.ReadGlobal(homeDir)
	if globalErr != nil {
		global = config.DefaultGlobal()
	}
	stateDir := stateDirOf(homeDir)
	checks := doctor{
		HomeDir:     homeDir,
		StateDir:    stateDir,
		IlotVersion: versionOfThisBinary(),
		Global:      global,
		GlobalErr:   globalErr,
		WorkspaceConfig: func() (config.Config, error) {
			currentWorkspace, _, err := selectedWorkspace(command.Selection.Workspace)
			if err != nil {
				return config.Config{}, err
			}
			return currentWorkspace.ConfigForStack(workspace.BaseStackName)
		},
		LookPath: lookPathIn,
		RunVersion: func(ctx context.Context, program string, arguments ...string) (string, error) {
			output, err := exec.CommandContext(ctx, program, arguments...).CombinedOutput()
			return strings.TrimSpace(string(output)), err
		},
		LookupHost: net.DefaultResolver.LookupHost,
		DaemonStatus: func(ctx context.Context) (daemon.Status, error) {
			return daemon.NewClient(daemon.Paths{StateDir: stateDir}.Socket()).Status(ctx)
		},
		ShellEnv: func(ctx context.Context) (map[string]string, error) {
			return shellenv.OfUser(global.ShellEnv.Args).Environment(ctx)
		},
		ListenOn: func(address string) error {
			listener, err := net.Listen("tcp", address)
			if err != nil {
				return err
			}
			return listener.Close()
		},
	}.run(ctx)
	if err := printDoctorChecks(os.Stdout, checks); err != nil {
		return err
	}
	fixCount := 0
	for _, check := range checks {
		if check.Status == checkFix {
			fixCount++
		}
	}
	switch fixCount {
	case 0:
		return nil
	case 1:
		return errors.New("1 check needs a fix")
	}
	return fmt.Errorf("%d checks need a fix", fixCount)
}

func printDoctorChecks(output io.Writer, checks []doctorCheck) error {
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	for _, check := range checks {
		fmt.Fprintf(table, "%s\t%s\t%s\n", check.Status, check.Name, check.Detail)
	}
	return table.Flush()
}

func (checks doctor) run(ctx context.Context) []doctorCheck {
	results := []doctorCheck{checks.git(ctx), checks.globalFile(), checks.stateFolder()}
	results = append(results, checks.oldState()...)
	daemonCheck, proxyCheck := checks.daemonAndProxy(ctx)
	if checks.GlobalErr != nil {
		// The defaults would test an address, a domain and shell flags that are maybe not the ones of the file.
		skipped := func(name string) doctorCheck {
			return doctorCheck{Status: checkSkip, Name: name, Detail: "the global file is broken: fix it, then run ilot doctor again"}
		}
		return append(results, daemonCheck, skipped("proxy"), skipped("name lookup"), skipped("shell"), skipped("apps"))
	}
	shellEnv, shellErr := checks.ShellEnv(ctx)
	results = append(results, daemonCheck, proxyCheck, checks.resolution(ctx), shellCheck(shellEnv, shellErr))
	return append(results, checks.toolsOfTheApps(ctx, shellEnv["PATH"])...)
}

func lookPathIn(program, pathList string) (string, error) {
	for _, dir := range filepath.SplitList(pathList) {
		candidate := filepath.Join(dir, program)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s is not in the PATH of the apps", program)
}

func (checks doctor) git(ctx context.Context) doctorCheck {
	version, err := checks.RunVersion(ctx, "git", "--version")
	if err != nil {
		return doctorCheck{Status: checkFix, Name: "git", Detail: fmt.Sprintf("git does not run: %v. Install git: ilot needs it to find the worktrees", err)}
	}
	return doctorCheck{Status: checkOK, Name: "git", Detail: version}
}

func (checks doctor) globalFile() doctorCheck {
	globalFile := config.GlobalFile(checks.HomeDir)
	if checks.GlobalErr != nil {
		return doctorCheck{Status: checkFix, Name: "global file", Detail: checks.GlobalErr.Error()}
	}
	if _, err := os.Stat(globalFile); errors.Is(err, fs.ErrNotExist) {
		return doctorCheck{Status: checkOK, Name: "global file", Detail: globalFile + " does not exist: the defaults apply"}
	}
	return doctorCheck{Status: checkOK, Name: "global file", Detail: globalFile}
}

func (checks doctor) stateFolder() doctorCheck {
	if err := os.MkdirAll(checks.StateDir, 0o700); err != nil {
		return doctorCheck{Status: checkFix, Name: "state folder", Detail: fmt.Sprintf("ilot cannot make %s: %v", checks.StateDir, err)}
	}
	probe, err := os.CreateTemp(checks.StateDir, ".doctor-*")
	if err != nil {
		return doctorCheck{Status: checkFix, Name: "state folder", Detail: fmt.Sprintf("ilot cannot write in %s: %v", checks.StateDir, err)}
	}
	probe.Close()
	os.Remove(probe.Name())
	if _, err := (state.Store{RootDir: checks.StateDir}).Load(); err != nil {
		return doctorCheck{Status: checkFix, Name: "state folder", Detail: err.Error()}
	}
	return doctorCheck{Status: checkOK, Name: "state folder", Detail: checks.StateDir}
}

// An older ilot kept one folder per stack, beside the files of the process manager that it ran.
// This ilot does not read them, so they only take room, and an old env.yaml stops every command.
func (checks doctor) oldState() []doctorCheck {
	var results []doctorCheck
	oldStacksDir := filepath.Join(checks.StateDir, "stacks")
	if entries, err := os.ReadDir(oldStacksDir); err == nil {
		var stackNames []string
		for _, entry := range entries {
			if entry.IsDir() {
				stackNames = append(stackNames, entry.Name())
			}
		}
		results = append(results, doctorCheck{Status: checkFix, Name: "old state", Detail: fmt.Sprintf("%s holds the stacks of an older ilot (%s), which this ilot does not read. Stop their apps with that ilot if they still run, then run: rm -rf %s", oldStacksDir, strings.Join(stackNames, ", "), shellQuoted(oldStacksDir))})
	}
	oldSupervisorDir := filepath.Join(checks.StateDir, "process-compose")
	if _, err := os.Stat(oldSupervisorDir); err == nil {
		results = append(results, doctorCheck{Status: checkFix, Name: "old state", Detail: fmt.Sprintf("%s holds the process-compose files of an older ilot. Run: rm -rf %s", oldSupervisorDir, shellQuoted(oldSupervisorDir))})
	}
	if oldPersonalFile, exists := config.OldPersonalFile(checks.HomeDir); exists {
		results = append(results, doctorCheck{Status: checkFix, Name: "old state", Detail: fmt.Sprintf("%s is the personal file of an older ilot, and every command stops while it exists. Move its content under workspaces.<name> of %s, then run: rm %s", oldPersonalFile, config.GlobalFileShown(), shellQuoted(oldPersonalFile))})
	}
	if len(results) == 0 {
		results = append(results, doctorCheck{Status: checkOK, Name: "old state", Detail: "none"})
	}
	return results
}

func shellQuoted(path string) string {
	if strings.ContainsAny(path, " '\"$`\\") {
		return "'" + strings.ReplaceAll(path, "'", `'"'"'`) + "'"
	}
	return path
}

func (checks doctor) daemonAndProxy(ctx context.Context) (daemonCheck, proxyCheck doctorCheck) {
	listen := checks.Global.Proxy.Listen
	status, err := checks.DaemonStatus(ctx)
	if errors.Is(err, daemon.ErrDaemonDown) {
		daemonCheck = doctorCheck{Status: checkOK, Name: "daemon", Detail: "does not run: ilot starts it when a command needs it"}
		if err := checks.ListenOn(listen); err != nil {
			return daemonCheck, doctorCheck{Status: checkFix, Name: "proxy", Detail: fmt.Sprintf("another program holds %s: %v. Stop it, or set proxy.listen to another address in %s", listen, err, config.GlobalFileShown())}
		}
		return daemonCheck, doctorCheck{Status: checkOK, Name: "proxy", Detail: listen + " is free"}
	}
	if err != nil {
		return doctorCheck{Status: checkFix, Name: "daemon", Detail: err.Error()}, doctorCheck{Status: checkSkip, Name: "proxy", Detail: "the daemon does not answer"}
	}
	daemonCheck = doctorCheck{Status: checkOK, Name: "daemon", Detail: fmt.Sprintf("version %s, process %d", status.Version, status.PID)}
	if status.Version != checks.IlotVersion {
		daemonCheck = doctorCheck{Status: checkFix, Name: "daemon", Detail: engine.DaemonWarnings(status, checks.IlotVersion, status.ProxyAddress)[0]}
	}
	proxyCheck = doctorCheck{Status: checkOK, Name: "proxy", Detail: status.ProxyAddress}
	statusOfThisVersion := status
	statusOfThisVersion.Version = checks.IlotVersion
	if warnings := engine.DaemonWarnings(statusOfThisVersion, checks.IlotVersion, listen); len(warnings) > 0 {
		proxyCheck = doctorCheck{Status: checkFix, Name: "proxy", Detail: warnings[0]}
	}
	return daemonCheck, proxyCheck
}

func (checks doctor) resolution(ctx context.Context) doctorCheck {
	host := "ilot-doctor." + checks.Global.Proxy.Domain
	lookupContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := checks.LookupHost(lookupContext, host)
	fixes := fmt.Sprintf("A browser still reaches the URLs, but an app that calls another one by its URL fails. Set proxy.domain in %s to a domain whose names all point to this machine, such as localtest.me, or use the port template for the calls between apps", config.GlobalFileShown())
	if err != nil {
		return doctorCheck{Status: checkFix, Name: "name lookup", Detail: fmt.Sprintf("%s does not resolve: %v. %s", host, err, fixes)}
	}
	for _, address := range addresses {
		if parsed, err := netip.ParseAddr(address); err != nil || !parsed.IsLoopback() {
			return doctorCheck{Status: checkFix, Name: "name lookup", Detail: fmt.Sprintf("%s resolves to %s, which is not this machine. %s", host, address, fixes)}
		}
	}
	return doctorCheck{Status: checkOK, Name: "name lookup", Detail: fmt.Sprintf("%s resolves to %s", host, strings.Join(addresses, ", "))}
}

func shellCheck(environment map[string]string, err error) doctorCheck {
	if err != nil {
		return doctorCheck{Status: checkFix, Name: "shell", Detail: fmt.Sprintf("the login shell gives no environment: %v. Fix the profile files, or set shell_env.args in %s", err, config.GlobalFileShown())}
	}
	return doctorCheck{Status: checkOK, Name: "shell", Detail: "the apps get PATH=" + environment["PATH"]}
}

// toolsOfTheApps tests the programs that some apps need: watchexec for an app with watch, and docker for a container app.
func (checks doctor) toolsOfTheApps(ctx context.Context, pathOfTheApps string) []doctorCheck {
	workspaceConfig, err := checks.WorkspaceConfig()
	if err != nil {
		return []doctorCheck{{Status: checkSkip, Name: "apps", Detail: fmt.Sprintf("no workspace to read: %v. Run ilot doctor in a repo of a workspace, or give --workspace, to test watchexec and docker", err)}}
	}
	var watched, containers []string
	for _, appName := range slices.Sorted(maps.Keys(workspaceConfig.Apps)) {
		app := workspaceConfig.Apps[appName]
		if len(app.Watch.Paths) > 0 {
			watched = append(watched, appName)
		}
		if !app.Container.IsZero() {
			containers = append(containers, appName)
		}
	}
	return []doctorCheck{
		checks.tool(ctx, pathOfTheApps, "watchexec", watched, "watch", []string{"--version"}, "install watchexec, for example with brew install watchexec, in a folder of the PATH of a login shell"),
		checks.tool(ctx, pathOfTheApps, "docker", containers, "container", []string{"version", "--format", "{{.Server.Version}}"}, "install Docker and start it"),
	}
}

func (checks doctor) tool(ctx context.Context, pathOfTheApps, program string, appNames []string, key string, versionArguments []string, installHint string) doctorCheck {
	if len(appNames) == 0 {
		return doctorCheck{Status: checkSkip, Name: program, Detail: "no app of the workspace has " + key}
	}
	needs := fmt.Sprintf("the apps with %s need it: %s", key, strings.Join(appNames, ", "))
	programPath, err := checks.LookPath(program, pathOfTheApps)
	if err != nil {
		return doctorCheck{Status: checkFix, Name: program, Detail: fmt.Sprintf("%v: %s. To fix: %s", err, needs, installHint)}
	}
	version, err := checks.RunVersion(ctx, programPath, versionArguments...)
	if err != nil {
		return doctorCheck{Status: checkFix, Name: program, Detail: fmt.Sprintf("%s does not answer: %v %s. %s", program, err, version, needs)}
	}
	return doctorCheck{Status: checkOK, Name: program, Detail: version}
}
