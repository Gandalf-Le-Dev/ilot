package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	// The global file, under the home folder. It is optional.
	GlobalFilePath = ".config/ilot/config.yaml"
	// The personal file of an older ilot. The workspaces key of the global file replaces it.
	oldPersonalFilePath = ".config/ilot/env.yaml"

	DefaultProxyListen = "127.0.0.1:4580"
	// Every name under localhost points to this machine, with no DNS setup.
	DefaultDomain    = "localhost"
	DefaultFirstPort = 20000
	DefaultLastPort  = 29999
)

// Global holds the values of the machine and the values that must stay out of a repo.
type Global struct {
	Proxy    Proxy     `yaml:"proxy"`
	Ports    PortRange `yaml:"ports"`
	ShellEnv ShellEnv  `yaml:"shell_env"`
	// By the name of a workspace.
	Workspaces map[string]WorkspaceSettings `yaml:"workspaces"`
}

type Proxy struct {
	// The address of the proxy. Change it only when another program uses the port.
	Listen string `yaml:"listen"`
	// The end of every URL. Every name under it must point to this machine.
	Domain string `yaml:"domain"`
}

// PortRange is the range that ilot takes the ports of the apps from.
type PortRange struct {
	First int `yaml:"first"`
	Last  int `yaml:"last"`
}

func (portRange PortRange) Contains(port int) bool {
	return port >= portRange.First && port <= portRange.Last
}

type ShellEnv struct {
	// The flags of the shell that gives the base environment of every app. Without the key, the flag is -l.
	Args []string `yaml:"args"`
}

type WorkspaceSettings struct {
	// The path of a member repo that is cloned at another place than the one that the root repo declares.
	Repos map[string]string `yaml:"repos"`
	// For every app of the workspace.
	Env map[string]string `yaml:"env"`
	// For one app: from the name of the app to its variables.
	Apps map[string]map[string]string `yaml:"apps"`
}

func defaultGlobal() Global {
	return Global{
		Proxy:      Proxy{Listen: DefaultProxyListen, Domain: DefaultDomain},
		Ports:      PortRange{First: DefaultFirstPort, Last: DefaultLastPort},
		ShellEnv:   ShellEnv{Args: []string{"-l"}},
		Workspaces: nil,
	}
}

// LoadGlobal reads the global file of a home folder. A missing file gives the defaults, and a missing key keeps its default.
func LoadGlobal(homeDir string) (Global, error) {
	globalFile := filepath.Join(homeDir, GlobalFilePath)
	oldPersonalFile := filepath.Join(homeDir, oldPersonalFilePath)
	if _, err := os.Stat(oldPersonalFile); err == nil {
		return Global{}, fmt.Errorf("%s is the personal file of an older ilot, and ilot no longer reads it: move its content to %s, under workspaces.<name of the workspace> (env, apps, and repos as <name>: <path>), then delete it", oldPersonalFile, globalFile)
	}
	global := defaultGlobal()
	content, err := os.ReadFile(globalFile)
	if errors.Is(err, fs.ErrNotExist) {
		return global, nil
	}
	if err != nil {
		return Global{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	if err := decoder.Decode(&global); err != nil && !errors.Is(err, io.EOF) {
		return Global{}, fmt.Errorf("parse %s: %w", globalFile, errors.Join(append([]error{err}, hintsForUnknownKeys(err)...)...))
	}
	if err := global.validate(); err != nil {
		return Global{}, fmt.Errorf("invalid %s: %w", globalFile, err)
	}
	return global, nil
}

func (global Global) validate() error {
	var problems []error
	if _, _, err := net.SplitHostPort(global.Proxy.Listen); err != nil {
		problems = append(problems, fmt.Errorf("proxy.listen %q is not an address such as %s: %w", global.Proxy.Listen, DefaultProxyListen, err))
	}
	if global.Proxy.Domain == "" {
		problems = append(problems, fmt.Errorf("proxy.domain is empty: remove the key to get %s", DefaultDomain))
	}
	if global.Ports.First < 1 || global.Ports.Last > 65535 || global.Ports.First > global.Ports.Last {
		problems = append(problems, fmt.Errorf("ports: first %d and last %d do not make a range of ports between 1 and 65535", global.Ports.First, global.Ports.Last))
	}
	return errors.Join(problems...)
}
