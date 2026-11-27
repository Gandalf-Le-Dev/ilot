package appconfig

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

const (
	ReservedPortFirst = 20000
	ReservedPortLast  = 29999
	DefaultRepo       = "platform"
)

type Config struct {
	Domain   string                       `yaml:"domain"`
	Repos    map[string]RepoConfig        `yaml:"repos"`
	Env      map[string]string            `yaml:"env"`
	External map[string]ExternalService   `yaml:"external"`
	Apps     map[string]App               `yaml:"apps"`
	RepoEnv  map[string]map[string]string `yaml:"-"`
}

type RepoConfig struct {
	Path   string `yaml:"path"`
	Config string `yaml:"config"`
}

type ExternalService struct {
	Port      int    `yaml:"port"`
	StartHint string `yaml:"start_hint"`
}

type App struct {
	Build      string            `yaml:"build"`
	Run        string            `yaml:"run"`
	Env        map[string]string `yaml:"env"`
	Ports      []string          `yaml:"ports"`
	Route      string            `yaml:"route"`
	Ready      ReadyCheck        `yaml:"ready"`
	DependsOn  []string          `yaml:"depends_on"`
	Shared     bool              `yaml:"shared"`
	Autostart  bool              `yaml:"autostart"`
	FixedPorts map[string]int    `yaml:"fixed_ports"`
	Dir        string            `yaml:"dir"`
	Watch      Watch             `yaml:"watch"`
	HomePath   string            `yaml:"home_path"`
	Repo       string            `yaml:"-"`
	Container  Container         `yaml:"container"`
	PerStack   bool              `yaml:"per_stack"`
}

type Container struct {
	Image         string            `yaml:"image"`
	Mounts        map[string]string `yaml:"mounts"`
	EnvFiles      []string          `yaml:"env_files"`
	Links         map[string]string `yaml:"links"`
	ExternalLinks map[string]string `yaml:"external_links"`
}

func (container Container) IsZero() bool {
	return container.Image == ""
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

func Load(configPath string) (Config, error) {
	config, err := Parse(configPath)
	if err != nil {
		return Config{}, err
	}
	if err := config.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", configPath, err)
	}
	return config, nil
}

func Parse(configPath string) (Config, error) {
	content, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		if isUnknownFieldError(err) {
			return Config{}, fmt.Errorf("parse %s: %w\nyour platform main checkout can be older than this file: pull it", configPath, err)
		}
		return Config{}, fmt.Errorf("parse %s: %w", configPath, err)
	}
	return config, nil
}

func isUnknownFieldError(err error) bool {
	var typeError *yaml.TypeError
	if !errors.As(err, &typeError) {
		return false
	}
	return slices.ContainsFunc(typeError.Errors, func(message string) bool { return strings.Contains(message, "not found in type") })
}

// The repo list and the domain come from platform's config only.
func Merge(configByRepo map[string]Config) (Config, error) {
	rootConfig := configByRepo[DefaultRepo]
	merged := Config{
		Domain:   rootConfig.Domain,
		Repos:    rootConfig.Repos,
		Env:      nil,
		External: map[string]ExternalService{},
		Apps:     map[string]App{},
		RepoEnv:  map[string]map[string]string{},
	}
	var problems []error
	repoByApp := map[string]string{}
	for _, repoName := range slices.Sorted(maps.Keys(configByRepo)) {
		repoConfig := configByRepo[repoName]
		merged.RepoEnv[repoName] = repoConfig.Env
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
	return merged, merged.Validate()
}

func (config Config) Validate() error {
	return errors.Join(
		config.validateNames(),
		config.validateFixedPorts(),
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

func (config Config) validateFixedPorts() error {
	var problems []error
	ownerByPort := map[int]string{}
	claimFixedPort := func(owner string, port int) {
		if port >= ReservedPortFirst && port <= ReservedPortLast {
			problems = append(problems, fmt.Errorf("%s: port %d is inside the reserved range %d-%d", owner, port, ReservedPortFirst, ReservedPortLast))
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
		if !app.Container.IsZero() {
			problems = append(problems, config.validateContainer(name, app)...)
			continue
		}
		for _, portName := range slices.Sorted(maps.Keys(app.FixedPorts)) {
			claimFixedPort(fmt.Sprintf("app %q", name), app.FixedPorts[portName])
			if !slices.Contains(app.Ports, portName) {
				problems = append(problems, fmt.Errorf("app %q: fixed port %q is not one of its ports %v", name, portName, app.Ports))
			}
		}
		if !app.Shared {
			continue
		}
		for _, portName := range app.Ports {
			if _, isFixed := app.FixedPorts[portName]; !isFixed {
				problems = append(problems, fmt.Errorf("shared app %q needs a fixed number for port %q", name, portName))
			}
		}
	}
	return errors.Join(problems...)
}

// A container has its own network namespace: its ports are inside it, so they never clash with the host's.
func (config Config) validateContainer(name string, app App) []error {
	var problems []error
	for _, portName := range app.Ports {
		if _, isFixed := app.FixedPorts[portName]; !isFixed {
			problems = append(problems, fmt.Errorf("container app %q needs a fixed number for port %q", name, portName))
		}
	}
	for _, linkedApp := range slices.Sorted(maps.Values(app.Container.Links)) {
		if _, isApp := config.Apps[linkedApp]; !isApp {
			problems = append(problems, fmt.Errorf("container app %q links to unknown app %q", name, linkedApp))
		}
	}
	return problems
}

func (config Config) validateApps() error {
	var problems []error
	for _, name := range slices.Sorted(maps.Keys(config.Apps)) {
		app := config.Apps[name]
		if app.Run == "" && app.Container.IsZero() {
			problems = append(problems, fmt.Errorf("app %q: missing run command", name))
		}
		if app.Route != "" && !slices.Contains(app.Ports, app.Route) {
			problems = append(problems, fmt.Errorf("app %q: route %q is not one of its ports %v", name, app.Route, app.Ports))
		}
		if app.Ready.Port != "" && !slices.Contains(app.Ports, app.Ready.Port) {
			problems = append(problems, fmt.Errorf("app %q: ready port %q is not one of its ports %v", name, app.Ready.Port, app.Ports))
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
