package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// The file of a repo. Its name and its place, the root of the repo, are fixed.
const FileName = "ilot.yaml"

// Config is the file of one repo, and also what Merge makes of the files of every repo of a stack.
type Config struct {
	// The keys of the workspace. ilot reads them from the main checkout of a repo only.
	// Name, Repos and Stack belong to the root repo of the workspace. The default name is the folder name of the root.
	Name  string        `yaml:"name"`
	Repos RepoPaths     `yaml:"repos"`
	Stack StackSettings `yaml:"stack"`
	// The path of the root repo, in the file of a member repo. The root must list the member in its repos.
	MemberOf string            `yaml:"member_of"`
	Env      map[string]string `yaml:"env"`
	// The prefix of every build, run and stop command of the repo of this file, for example "mise exec --".
	Wrap     string                       `yaml:"wrap"`
	External map[string]ExternalService   `yaml:"external"`
	Apps     map[string]App               `yaml:"apps"`
	RepoEnv  map[string]map[string]string `yaml:"-"`
	RepoWrap map[string]string            `yaml:"-"`
}

type StackSettings struct {
	// A regular expression. Its first match in the name of a branch is the name of the stack of a worktree on that branch.
	// Without it, or without a match, the whole branch name is the stack name.
	Pattern string `yaml:"pattern"`
}

// RepoPaths gives the path of each member repo, relative to the main checkout of the root repo.
type RepoPaths map[string]string

func (repoPaths *RepoPaths) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: repos is a map from the name of a repo to its path, for example {web: ../web}", node.Line)
	}
	paths := RepoPaths{}
	for index := 0; index+1 < len(node.Content); index += 2 {
		name, path := node.Content[index], node.Content[index+1]
		if path.Kind != yaml.ScalarNode {
			return fmt.Errorf("line %d: repos.%s is now the path of the repo, relative to this repo, for example %s: ../%s. The keys path and config are gone: the file of a repo is always %s at its root", path.Line, name.Value, name.Value, name.Value, FileName)
		}
		paths[name.Value] = path.Value
	}
	*repoPaths = paths
	return nil
}

type ExternalService struct {
	Port      int    `yaml:"port"`
	StartHint string `yaml:"start_hint"`
}

type App struct {
	Build     string            `yaml:"build"`
	Run       string            `yaml:"run"`
	Stop      string            `yaml:"stop"`
	Env       map[string]string `yaml:"env"`
	Ports     []string          `yaml:"ports"`
	Route     string            `yaml:"route"`
	Ready     ReadyCheck        `yaml:"ready"`
	DependsOn []string          `yaml:"depends_on"`
	// Which stack serves the app to the apps that depend on it: ScopeBase, ScopeStack, or empty.
	Scope      string         `yaml:"scope"`
	Autostart  bool           `yaml:"autostart"`
	FixedPorts map[string]int `yaml:"fixed_ports"`
	Dir        string         `yaml:"dir"`
	Watch      Watch          `yaml:"watch"`
	HomePath   string         `yaml:"home_path"`
	Repo       string         `yaml:"-"`
	Container  Container      `yaml:"container"`
}

const (
	// One copy, in the base stack, for every stack.
	ScopeBase = "base"
	// An app that depends on this app always gets the copy of its own stack.
	ScopeStack = "stack"
)

type Container struct {
	Image string `yaml:"image"`
	// From the name of a port to its number inside the container. ilot publishes each one on a port of the stack.
	Ports map[string]int `yaml:"ports"`
	// From a path on this machine to a path in the container.
	Mounts   map[string]string `yaml:"mounts"`
	EnvFiles []string          `yaml:"env_files"`
	// More flags for docker run.
	Args []string `yaml:"args"`
}

func (container Container) IsZero() bool {
	return container.Image == ""
}

// PortNames gives the ports that ilot allocates for the app. A container declares them with their number inside the container.
func (app App) PortNames() []string {
	if app.Container.IsZero() {
		return app.Ports
	}
	return slices.Sorted(maps.Keys(app.Container.Ports))
}

type Watch struct {
	Paths []string `yaml:"paths"`
	Exts  []string `yaml:"exts"`
}

type ReadyCheck struct {
	Port    string `yaml:"port"`
	Path    string `yaml:"path"`
	Command string `yaml:"command"`
}

var dnsLabelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func Parse(configPath string) (Config, error) {
	content, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", configPath, errors.Join(append([]error{err}, hintsForUnknownKeys(err)...)...))
	}
	return config, nil
}

var unknownKeyPattern = regexp.MustCompile(`field (\S+) not found in type \w+\.(\w+)`)

// What replaced a key that an older ilot.yaml can still hold, by the type that held it.
var replacementOfRemovedKey = map[string]map[string]string{
	"App": {
		"shared":             "write scope: base, for one copy in the base stack for every stack",
		"per_stack":          "write scope: stack, for an app that the apps of a stack always get from their own stack",
		"gateway":            "it is gone: the port and url templates give the address of an app",
		"gateway_aliases":    "it is gone: the port and url templates give the address of an app",
		"temporal_namespace": "it is gone, with no replacement",
	},
	"Config": {
		"gateway_base_file": "it is gone: the port and url templates give the address of an app",
		"domain":            "it moved to proxy.domain in ~/" + GlobalFilePath,
	},
	"Global": {
		"env":   "it moved under workspaces.<name of the workspace>.env",
		"apps":  "it moved under workspaces.<name of the workspace>.apps",
		"repos": "it moved under workspaces.<name of the workspace>.repos, as <name>: <path>",
	},
	"ExternalService": {
		"gateway_aliases": "it is gone: the port template gives the port of an external service",
	},
	"Container": {
		"links":          "it is gone: use the host and port templates for the address of another app",
		"external_links": "it is gone: use the host and port templates for the address of another app",
	},
}

// The usual cause of an unknown key is a key that a newer ilot removed or renamed, so each one gets its replacement.
func hintsForUnknownKeys(err error) []error {
	typeError, isTypeError := errors.AsType[*yaml.TypeError](err)
	if !isTypeError {
		return nil
	}
	var hints []error
	hasUnknownKey := false
	for _, message := range typeError.Errors {
		match := unknownKeyPattern.FindStringSubmatch(message)
		if match == nil {
			continue
		}
		hasUnknownKey = true
		if replacement, isRemoved := replacementOfRemovedKey[match[2]][match[1]]; isRemoved {
			hints = append(hints, fmt.Errorf("%s: %s", match[1], replacement))
		}
	}
	if hasUnknownKey && len(hints) == 0 {
		hints = append(hints, errors.New("the README of ilot lists the keys of this file"))
	}
	return hints
}

// Merge makes one configuration of the files of the repos of a stack. A fixed port must be outside the range that ilot allocates from.
func Merge(configByRepo map[string]Config, allocatedPorts PortRange) (Config, error) {
	merged := Config{
		Name:     "",
		Repos:    nil,
		Stack:    StackSettings{Pattern: ""},
		MemberOf: "",
		Env:      nil,
		External: map[string]ExternalService{},
		Apps:     map[string]App{},
		RepoEnv:  map[string]map[string]string{},
		RepoWrap: map[string]string{},
	}
	var problems []error
	repoByApp := map[string]string{}
	for _, repoName := range slices.Sorted(maps.Keys(configByRepo)) {
		repoConfig := configByRepo[repoName]
		merged.RepoEnv[repoName] = repoConfig.Env
		if repoConfig.Wrap != "" {
			merged.RepoWrap[repoName] = repoConfig.Wrap
		}
		for _, appName := range slices.Sorted(maps.Keys(repoConfig.Apps)) {
			if otherRepo, isTaken := repoByApp[appName]; isTaken {
				problems = append(problems, fmt.Errorf("app %q is declared by both %s and %s", appName, otherRepo, repoName))
				continue
			}
			repoByApp[appName] = repoName
			app := repoConfig.Apps[appName]
			app.Repo = repoName
			merged.Apps[appName] = app
		}
		for externalName, externalService := range repoConfig.External {
			if _, isTaken := merged.External[externalName]; isTaken {
				problems = append(problems, fmt.Errorf("external service %q is declared by several repos, including %s", externalName, repoName))
			}
			merged.External[externalName] = externalService
		}
	}
	if err := errors.Join(problems...); err != nil {
		return Config{}, err
	}
	return merged, merged.Validate(allocatedPorts)
}

func (config Config) Validate(allocatedPorts PortRange) error {
	return errors.Join(
		config.validateNames(),
		config.validateFixedPorts(allocatedPorts),
		config.validateApps(),
		config.validateDependencyGraph(),
	)
}

func (config Config) validateNames() error {
	var problems []error
	for _, name := range slices.Sorted(maps.Keys(config.Apps)) {
		if _, isExternal := config.External[name]; isExternal {
			problems = append(problems, fmt.Errorf("%q is both an app and an external service", name))
		}
		if !dnsLabelPattern.MatchString(name) {
			problems = append(problems, fmt.Errorf("app %q: name must be a DNS label (lowercase letters, digits, dashes)", name))
		}
	}
	return errors.Join(problems...)
}

func (config Config) validateFixedPorts(allocatedPorts PortRange) error {
	var problems []error
	ownerByPort := map[int]string{}
	claimFixedPort := func(owner string, port int) {
		if allocatedPorts.Contains(port) {
			problems = append(problems, fmt.Errorf("%s: port %d is inside the range %d-%d, which ilot gives to the apps", owner, port, allocatedPorts.First, allocatedPorts.Last))
		}
		if otherOwner, isTaken := ownerByPort[port]; isTaken {
			problems = append(problems, fmt.Errorf("%s and %s both use port %d", otherOwner, owner, port))
		}
		ownerByPort[port] = owner
	}
	for _, name := range slices.Sorted(maps.Keys(config.External)) {
		claimFixedPort(fmt.Sprintf("external service %q", name), config.External[name].Port)
	}
	for _, name := range slices.Sorted(maps.Keys(config.Apps)) {
		app := config.Apps[name]
		for _, portName := range slices.Sorted(maps.Keys(app.FixedPorts)) {
			claimFixedPort(fmt.Sprintf("app %q", name), app.FixedPorts[portName])
			if !slices.Contains(app.PortNames(), portName) {
				problems = append(problems, fmt.Errorf("app %q: fixed port %q is not one of its ports %v", name, portName, app.PortNames()))
			}
		}
	}
	return errors.Join(problems...)
}

func validateContainer(name string, app App) []error {
	var problems []error
	if len(app.Ports) > 0 {
		problems = append(problems, fmt.Errorf("app %q: a container app declares its ports in container.ports, with the port inside the container, for example {http: 8080}", name))
	}
	for _, portName := range slices.Sorted(maps.Keys(app.Container.Ports)) {
		if portInContainer := app.Container.Ports[portName]; portInContainer < 1 || portInContainer > 65535 {
			problems = append(problems, fmt.Errorf("app %q: container port %q has the number %d", name, portName, portInContainer))
		}
	}
	return problems
}

func (config Config) validateApps() error {
	var problems []error
	for _, name := range slices.Sorted(maps.Keys(config.Apps)) {
		app := config.Apps[name]
		if app.Scope != "" && app.Scope != ScopeBase && app.Scope != ScopeStack {
			problems = append(problems, fmt.Errorf("app %q: scope %q is not %s or %s", name, app.Scope, ScopeBase, ScopeStack))
		}
		if app.Run == "" && app.Container.IsZero() {
			problems = append(problems, fmt.Errorf("app %q: missing run command", name))
		}
		if !app.Container.IsZero() {
			problems = append(problems, validateContainer(name, app)...)
		}
		if app.Stop != "" && !app.Container.IsZero() {
			problems = append(problems, fmt.Errorf("app %q: stop is for an app with a run command: ilot stops a container app itself", name))
		}
		if app.Route != "" && !slices.Contains(app.PortNames(), app.Route) {
			problems = append(problems, fmt.Errorf("app %q: route %q is not one of its ports %v", name, app.Route, app.PortNames()))
		}
		if app.Ready.Port != "" && !slices.Contains(app.PortNames(), app.Ready.Port) {
			problems = append(problems, fmt.Errorf("app %q: ready port %q is not one of its ports %v", name, app.Ready.Port, app.PortNames()))
		}
		if app.HomePath != "" && app.Route == "" {
			problems = append(problems, fmt.Errorf("app %q: home_path needs a route", name))
		}
		if app.HomePath != "" && !strings.HasPrefix(app.HomePath, "/") {
			problems = append(problems, fmt.Errorf("app %q: home_path %q must start with /", name, app.HomePath))
		}
		if len(app.Watch.Exts) > 0 && len(app.Watch.Paths) == 0 {
			problems = append(problems, fmt.Errorf("app %q: watch needs paths", name))
		}
		for _, dependency := range app.DependsOn {
			_, isApp := config.Apps[dependency]
			_, isExternal := config.External[dependency]
			if !isApp && !isExternal {
				problems = append(problems, fmt.Errorf("app %q: unknown dependency %q", name, dependency))
			}
		}
	}
	return errors.Join(problems...)
}

func (config Config) validateDependencyGraph() error {
	const (
		visiting = iota + 1
		visited
	)
	visitState := map[string]int{}
	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		if _, isApp := config.Apps[name]; !isApp {
			return nil
		}
		switch visitState[name] {
		case visiting:
			return fmt.Errorf("dependency cycle: %v", append(path, name))
		case visited:
			return nil
		}
		visitState[name] = visiting
		for _, dependency := range config.Apps[name].DependsOn {
			if err := visit(dependency, append(path, name)); err != nil {
				return err
			}
		}
		visitState[name] = visited
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(config.Apps)) {
		if err := visit(name, nil); err != nil {
			return err
		}
	}
	return nil
}
