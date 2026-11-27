// Package workspace finds the repos of the workspace, their worktrees and the stack of a folder.
package workspace

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/appconfig"
	"github.com/Gandalf-Le-Dev/ilot/internal/personal"
)

const (
	PersonalSettingsPath = ".config/ilot/env.yaml"
	PlatformConfigPath   = "devbox/ilot.yaml"
	defaultPlatformPath  = "~/src/platform"
)

type Workspace struct {
	HomeDir     string
	Settings    personal.Settings
	RepoPaths   map[string]string
	RepoConfigs map[string]appconfig.RepoConfig

	git *Git
}

// The repo list is read from platform's main checkout: the stack, and so its checkouts, are not known yet.
func Load(homeDir string, git *Git) (Workspace, error) {
	settings, err := personal.Load(filepath.Join(homeDir, PersonalSettingsPath))
	if err != nil {
		return Workspace{}, err
	}
	platformPath, err := Canonical(expandHome(cmp.Or(settings.Repos[appconfig.DefaultRepo].Path, defaultPlatformPath), homeDir))
	if err != nil {
		return Workspace{}, err
	}
	rootConfig, err := appconfig.Parse(filepath.Join(platformPath, PlatformConfigPath))
	if errors.Is(err, fs.ErrNotExist) {
		return Workspace{}, fmt.Errorf("%w: if platform is cloned elsewhere, set repos.platform.path in ~/%s", err, PersonalSettingsPath)
	}
	if err != nil {
		return Workspace{}, err
	}
	repoConfigs := maps.Clone(rootConfig.Repos)
	if repoConfigs == nil {
		repoConfigs = map[string]appconfig.RepoConfig{}
	}
	repoConfigs[appconfig.DefaultRepo] = appconfig.RepoConfig{Path: platformPath, Config: PlatformConfigPath}
	for repoName, personalRepo := range settings.Repos {
		if _, isShared := repoConfigs[repoName]; isShared || personalRepo.Path == "" || personalRepo.Config == "" {
			continue
		}
		repoConfigs[repoName] = personalRepo
	}
	repoPaths := map[string]string{}
	for repoName, repoConfig := range repoConfigs {
		repoPaths[repoName], err = Canonical(expandHome(cmp.Or(settings.Repos[repoName].Path, repoConfig.Path), homeDir))
		if err != nil {
			return Workspace{}, err
		}
	}
	return Workspace{HomeDir: homeDir, Settings: settings, RepoPaths: repoPaths, RepoConfigs: repoConfigs, git: git}, nil
}

func expandHome(path, homeDir string) string {
	if rest, isUnderHome := strings.CutPrefix(path, "~/"); isUnderHome {
		return filepath.Join(homeDir, rest)
	}
	return path
}

func (workspace Workspace) ConfigForStack(stackName string) (appconfig.Config, error) {
	checkoutByRepo, err := workspace.CheckoutsForStack(stackName)
	if err != nil {
		return appconfig.Config{}, err
	}
	configByRepo := map[string]appconfig.Config{}
	for _, repoName := range slices.Sorted(maps.Keys(checkoutByRepo)) {
		configFile := workspace.RepoConfigs[repoName].Config
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

func (workspace Workspace) ReposNotCloned() map[string]string {
	pathByRepo := map[string]string{}
	for repoName, repoPath := range workspace.RepoPaths {
		if isDeleted(repoPath) {
			pathByRepo[repoName] = repoPath
		}
	}
	return pathByRepo
}

// CheckoutsForStack gives the checkout of each cloned repo for a stack: the worktree of the stack, else the main checkout.
func (workspace Workspace) CheckoutsForStack(stackName string) (map[string]Worktree, error) {
	checkoutByRepo := map[string]Worktree{}
	var problems []error
	for _, repoName := range slices.Sorted(maps.Keys(workspace.RepoPaths)) {
		if isDeleted(workspace.RepoPaths[repoName]) {
			continue
		}
		worktrees, err := workspace.git.Worktrees(repoName, workspace.RepoPaths[repoName])
		if err != nil {
			return nil, err
		}
		checkout, err := checkoutForStack(worktrees, stackName)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		checkoutByRepo[repoName] = checkout
	}
	return checkoutByRepo, errors.Join(problems...)
}

func checkoutForStack(worktrees []Worktree, stackName string) (Worktree, error) {
	mainCheckout := worktrees[0]
	if stackName == MasterStackName || stackName == SharedStackName {
		return mainCheckout, nil
	}
	var matching []Worktree
	for _, linkedWorktree := range worktrees[1:] {
		name, err := StackName(linkedWorktree)
		if err == nil && name == stackName {
			matching = append(matching, linkedWorktree)
		}
	}
	switch len(matching) {
	case 0:
		return mainCheckout, nil
	case 1:
		return matching[0], nil
	}
	var matchingDirs []string
	for _, worktree := range matching {
		matchingDirs = append(matchingDirs, worktree.Dir)
	}
	return Worktree{}, SeveralWorktreesError{Repo: mainCheckout.Repo, StackName: stackName, WorktreeDirs: matchingDirs}
}

type SeveralWorktreesError struct {
	Repo         string
	StackName    string
	WorktreeDirs []string
}

func (err SeveralWorktreesError) Error() string {
	return fmt.Sprintf("repo %s has several worktrees for stack %s, keep one: %s", err.Repo, err.StackName, strings.Join(err.WorktreeDirs, ", "))
}

func IsSeveralWorktrees(err error) bool {
	_, isSeveralWorktrees := errors.AsType[SeveralWorktreesError](err)
	return isSeveralWorktrees
}

// RepoOf names the repo of a worktree that Locate found. The main checkout tells the repo, so a repo path that is itself a linked worktree works too.
func (workspace Workspace) RepoOf(worktree Worktree) (string, error) {
	for _, repoName := range slices.Sorted(maps.Keys(workspace.RepoPaths)) {
		if isDeleted(workspace.RepoPaths[repoName]) {
			continue
		}
		worktrees, err := workspace.git.Worktrees(repoName, workspace.RepoPaths[repoName])
		if err == nil && worktrees[0].Dir == worktree.MainDir {
			return repoName, nil
		}
	}
	return "", fmt.Errorf("%s is in no repo that ilot knows: if it is one of %v cloned elsewhere, set repos.<name>.path in ~/%s; for a personal repo, set repos.<name>.path and repos.<name>.config there", worktree.Dir, slices.Sorted(maps.Keys(workspace.RepoPaths)), PersonalSettingsPath)
}

func (workspace Workspace) Locate(dirInsideWorktree string) (Worktree, error) {
	return workspace.git.Locate(dirInsideWorktree)
}

// MainCheckout gives the main checkout of platform, which is the master stack.
func (workspace Workspace) MainCheckout() (Worktree, error) {
	worktrees, err := workspace.git.Worktrees(appconfig.DefaultRepo, workspace.RepoPaths[appconfig.DefaultRepo])
	if err != nil {
		return Worktree{}, err
	}
	return worktrees[0], nil
}

// StackOf gives the stack of a folder and the worktree that holds it.
// A command that works across stacks, and a folder that is in no repo of the workspace, get the master stack.
func (workspace Workspace) StackOf(dir string, isCrossStack bool, notes io.Writer) (string, Worktree, error) {
	if isCrossStack {
		return workspace.masterStack()
	}
	worktree, err := workspace.git.Locate(dir)
	if err != nil {
		fmt.Fprintln(notes, "not in a checkout: using the master stack")
		return workspace.masterStack()
	}
	if isSameFolder(worktree.Dir, workspace.HomeDir) {
		fmt.Fprintln(notes, "the home folder is a checkout, not a known repo: using the master stack")
		return workspace.masterStack()
	}
	if worktree.Repo, err = workspace.RepoOf(worktree); err != nil {
		return "", Worktree{}, err
	}
	stackName, err := StackName(worktree)
	return stackName, worktree, err
}

func (workspace Workspace) masterStack() (string, Worktree, error) {
	mainCheckout, err := workspace.MainCheckout()
	return MasterStackName, mainCheckout, err
}

func isSameFolder(firstDir, secondDir string) bool {
	firstInfo, err := os.Stat(firstDir)
	if err != nil {
		return false
	}
	secondInfo, err := os.Stat(secondDir)
	return err == nil && os.SameFile(firstInfo, secondInfo)
}
