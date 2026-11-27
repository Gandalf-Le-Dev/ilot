package main

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/appconfig"
	"github.com/Gandalf-Le-Dev/ilot/internal/checkouts"
	"github.com/Gandalf-Le-Dev/ilot/internal/personal"
	"github.com/Gandalf-Le-Dev/ilot/internal/realpath"
	"github.com/Gandalf-Le-Dev/ilot/internal/stack"
)

const (
	personalSettingsPath = ".config/ilot/env.yaml"
	defaultPlatformPath  = "~/src/platform"
	platformConfigPath   = "devbox/ilot.yaml"
)

type workspace struct {
	homeDir     string
	settings    personal.Settings
	repoPaths   map[string]string
	repoConfigs map[string]appconfig.RepoConfig
}

// The repo list is read from platform's main checkout: the stack, and so its checkouts, are not known yet.
func loadWorkspace(homeDir string) (workspace, error) {
	settings, err := personal.Load(filepath.Join(homeDir, personalSettingsPath))
	if err != nil {
		return workspace{}, err
	}
	platformPath := realpath.Of(expandHome(cmp.Or(settings.Repos[appconfig.DefaultRepo].Path, defaultPlatformPath), homeDir))
	rootConfig, err := appconfig.Parse(filepath.Join(platformPath, platformConfigPath))
	if errors.Is(err, fs.ErrNotExist) {
		return workspace{}, fmt.Errorf("%w: if platform is cloned elsewhere, set repos.platform.path in ~/%s", err, personalSettingsPath)
	}
	if err != nil {
		return workspace{}, err
	}
	repoConfigs := maps.Clone(rootConfig.Repos)
	if repoConfigs == nil {
		repoConfigs = map[string]appconfig.RepoConfig{}
	}
	repoConfigs[appconfig.DefaultRepo] = appconfig.RepoConfig{Path: platformPath, Config: platformConfigPath}
	for repoName, personalRepo := range settings.Repos {
		if _, isShared := repoConfigs[repoName]; isShared || personalRepo.Path == "" || personalRepo.Config == "" {
			continue
		}
		repoConfigs[repoName] = personalRepo
	}
	repoPaths := map[string]string{}
	for repoName, repoConfig := range repoConfigs {
		repoPaths[repoName] = realpath.Of(expandHome(cmp.Or(settings.Repos[repoName].Path, repoConfig.Path), homeDir))
	}
	return workspace{homeDir: homeDir, settings: settings, repoPaths: repoPaths, repoConfigs: repoConfigs}, nil
}

func (currentWorkspace workspace) configForStack(stackName string) (appconfig.Config, error) {
	checkoutByRepo, err := checkouts.ForStack(currentWorkspace.repoPaths, stackName)
	if err != nil {
		return appconfig.Config{}, err
	}
	configByRepo := map[string]appconfig.Config{}
	for _, repoName := range slices.Sorted(maps.Keys(checkoutByRepo)) {
		configFile := currentWorkspace.repoConfigs[repoName].Config
		if configFile == "" {
			continue
		}
		repoConfig, err := appconfig.Parse(filepath.Join(checkoutByRepo[repoName].Dir, configFile))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return appconfig.Config{}, err
		}
		configByRepo[repoName] = repoConfig
	}
	return appconfig.Merge(configByRepo)
}

func (currentWorkspace workspace) checkoutsForStack(stackName string) (map[string]checkouts.Checkout, error) {
	return checkouts.ForStack(currentWorkspace.repoPaths, stackName)
}

func expandHome(path, homeDir string) string {
	if rest, isUnderHome := strings.CutPrefix(path, "~/"); isUnderHome {
		return filepath.Join(homeDir, rest)
	}
	return path
}

// The Docker daemon runs on the devhost, where devbox's home is ~/devbox: a bind mount must name the host path.
func hostPathFor(devboxHomeDir, login string) func(devboxPath string) string {
	return func(devboxPath string) string {
		if devboxPath == devboxHomeDir {
			return filepath.Join("/home", login, "devbox")
		}
		if rest, isUnderHome := strings.CutPrefix(devboxPath, devboxHomeDir+"/"); isUnderHome {
			return filepath.Join("/home", login, "devbox", rest)
		}
		return devboxPath
	}
}

func hasMiseConfig(checkoutDir string) bool {
	_, err := os.Stat(filepath.Join(checkoutDir, "mise.toml"))
	return err == nil
}

func (currentWorkspace workspace) reposNotCloned() map[string]string {
	pathByRepo := map[string]string{}
	for repoName, repoPath := range currentWorkspace.repoPaths {
		if _, err := os.Stat(repoPath); errors.Is(err, fs.ErrNotExist) {
			pathByRepo[repoName] = repoPath
		}
	}
	return pathByRepo
}

type folderLocation int

const (
	outsideAnyCheckout folderLocation = iota
	insideHomeCheckout
	insideCheckout
)

func (currentWorkspace workspace) locationOf(dir string) folderLocation {
	output, err := stack.GitOutput(dir, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return outsideAnyCheckout
	}
	if isSameFolder(strings.TrimSpace(output), currentWorkspace.homeDir) {
		return insideHomeCheckout
	}
	return insideCheckout
}

func isSameFolder(firstDir, secondDir string) bool {
	firstInfo, err := os.Stat(firstDir)
	if err != nil {
		return false
	}
	secondInfo, err := os.Stat(secondDir)
	return err == nil && os.SameFile(firstInfo, secondInfo)
}

func platformMainCheckout(currentWorkspace workspace) (stack.Worktree, error) {
	platformCheckout, err := stack.Locate(currentWorkspace.repoPaths[appconfig.DefaultRepo])
	if err != nil {
		return stack.Worktree{}, err
	}
	if platformCheckout.WorktreeDir == platformCheckout.MainCheckoutDir {
		return platformCheckout, nil
	}
	return stack.Locate(platformCheckout.MainCheckoutDir)
}

func currentFolder() (string, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return realpath.Of(workingDir), nil
}
