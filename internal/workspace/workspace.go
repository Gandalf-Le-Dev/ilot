// Package workspace finds the workspace of a folder: its repos, their worktrees and the stack of each worktree.
package workspace

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
)

// Where the file of the root repo was before a repo file had one fixed place.
const oldRootFilePath = "devbox/ilot.yaml"

// Workspace is the set of repos that share stacks, ports and one name.
type Workspace struct {
	Name string
	// The main checkout of the root repo, whose file names the workspace and lists the other repos.
	RootDir string
	HomeDir string
	// The global file, and what it holds for this workspace.
	Global   config.Global
	Settings config.WorkspaceSettings
	// The path of each repo. The root repo has the name of the workspace.
	RepoPaths map[string]string
	// The stack.pattern of the root repo. It is nil without the key.
	StackPattern *regexp.Regexp
	// The worktrees of each stack, from the state. Find leaves it empty.
	Bindings Bindings

	git *Git
}

// Find gives the workspace of a folder and the worktree that holds the folder.
// It reads the ilot.yaml of the main checkout of the repo of the folder, and follows member_of to the root repo.
func Find(dir, homeDir string, global config.Global, git *Git) (Workspace, Worktree, error) {
	current, err := git.Locate(dir)
	if err != nil {
		return Workspace{}, Worktree{}, fmt.Errorf("%w: ilot finds the workspace from the %s of the repo of the current folder", err, config.FileName)
	}
	ownFile := filepath.Join(current.MainDir, config.FileName)
	ownConfig, err := config.Parse(ownFile)
	if errors.Is(err, fs.ErrNotExist) {
		return Workspace{}, Worktree{}, errors.Join(fmt.Errorf("the repo at %s has no %s: ilot reads the %s of the main checkout to find the workspace", current.MainDir, config.FileName, config.FileName), oldLayoutHint(current.MainDir))
	}
	if err != nil {
		return Workspace{}, Worktree{}, err
	}
	rootDir, rootFile, rootConfig := current.MainDir, ownFile, ownConfig
	if ownConfig.MemberOf != "" {
		if ownConfig.Name != "" || len(ownConfig.Repos) > 0 || ownConfig.Stack.Pattern != "" {
			return Workspace{}, Worktree{}, fmt.Errorf("%s has member_of, so it is the file of a member repo: name, repos and stack belong to the file of the root repo", ownFile)
		}
		rootDir, err = git.rootDirOf(ownFile, ownConfig.MemberOf, homeDir)
		if err != nil {
			return Workspace{}, Worktree{}, err
		}
		rootFile = filepath.Join(rootDir, config.FileName)
		rootConfig, err = config.Parse(rootFile)
		if errors.Is(err, fs.ErrNotExist) {
			return Workspace{}, Worktree{}, fmt.Errorf("%s names its root repo with member_of: %s, but %s does not exist", ownFile, ownConfig.MemberOf, rootFile)
		}
		if err != nil {
			return Workspace{}, Worktree{}, err
		}
		if rootConfig.MemberOf != "" {
			return Workspace{}, Worktree{}, fmt.Errorf("%s names its root repo with member_of: %s, but %s has a member_of too: a root repo has none", ownFile, ownConfig.MemberOf, rootFile)
		}
	}
	name, err := workspaceName(rootConfig.Name, rootDir, rootFile)
	if err != nil {
		return Workspace{}, Worktree{}, err
	}
	var stackPattern *regexp.Regexp
	if rootConfig.Stack.Pattern != "" {
		stackPattern, err = regexp.Compile(rootConfig.Stack.Pattern)
		if err != nil {
			return Workspace{}, Worktree{}, fmt.Errorf("%s: stack.pattern is not a regular expression: %w", rootFile, err)
		}
	}
	settings := global.Workspaces[name]
	for repoName := range settings.Repos {
		if _, isDeclared := rootConfig.Repos[repoName]; !isDeclared {
			return Workspace{}, Worktree{}, fmt.Errorf("~/%s: workspaces.%s.repos.%s gives a path to a repo that %s does not list under repos", config.GlobalFilePath, name, repoName, rootFile)
		}
	}
	repoPaths := map[string]string{name: rootDir}
	for repoName, declaredPath := range rootConfig.Repos {
		if repoName == name {
			return Workspace{}, Worktree{}, fmt.Errorf("%s: the repo %q has the name of the workspace, which is the name of the root repo: rename it, or set name", rootFile, repoName)
		}
		repoPaths[repoName], err = Canonical(pathFrom(rootDir, cmp.Or(settings.Repos[repoName], declaredPath), homeDir))
		if err != nil {
			return Workspace{}, Worktree{}, err
		}
	}
	workspace := Workspace{Name: name, RootDir: rootDir, HomeDir: homeDir, Global: global, Settings: settings, RepoPaths: repoPaths, StackPattern: stackPattern, Bindings: nil, git: git}
	current.Repo, err = workspace.repoOf(current)
	if err != nil && ownConfig.MemberOf == "" {
		return Workspace{}, Worktree{}, err
	}
	if err != nil {
		suggestedPath, relErr := filepath.Rel(rootDir, current.MainDir)
		if relErr != nil {
			suggestedPath = current.MainDir
		}
		return Workspace{}, Worktree{}, fmt.Errorf("%s names its root repo with member_of: %s, but %s does not list this repo: add it there under repos, for example %s: %s", ownFile, ownConfig.MemberOf, rootFile, filepath.Base(current.MainDir), suggestedPath)
	}
	if err := workspace.checkEveryMemberNamesTheRoot(rootFile); err != nil {
		return Workspace{}, Worktree{}, err
	}
	return workspace, current, nil
}

func oldLayoutHint(mainCheckoutDir string) error {
	if _, err := os.Stat(filepath.Join(mainCheckoutDir, oldRootFilePath)); err != nil {
		return nil
	}
	return fmt.Errorf("%s is the old place of the file: move it to %s at the root of the repo", oldRootFilePath, config.FileName)
}

// rootDirOf follows the member_of of a member file to the main checkout of the root repo.
func (git *Git) rootDirOf(memberFile, memberOf, homeDir string) (string, error) {
	declaredRootDir, err := Canonical(pathFrom(filepath.Dir(memberFile), memberOf, homeDir))
	if err != nil {
		return "", err
	}
	rootWorktrees, err := git.Worktrees("", declaredRootDir)
	if err != nil {
		return "", fmt.Errorf("%s names its root repo with member_of: %s, but %s is not a git checkout: %w", memberFile, memberOf, declaredRootDir, err)
	}
	return rootWorktrees[0].Dir, nil
}

func workspaceName(declaredName, rootDir, rootFile string) (string, error) {
	if declaredName != "" {
		if slug(declaredName) != declaredName {
			return "", fmt.Errorf("%s: the name %q goes in the URL of every app, so it takes lowercase letters, digits and dashes only, %d characters at most", rootFile, declaredName, dnsLabelMaxLength)
		}
		return declaredName, nil
	}
	name := slug(filepath.Base(rootDir))
	if name == "" {
		return "", fmt.Errorf("the folder name of %s gives no name for the workspace: set name in %s", rootDir, rootFile)
	}
	return name, nil
}

// The link goes both ways: a member that does not name the root would be a workspace of its own when a command runs in it.
func (workspace Workspace) checkEveryMemberNamesTheRoot(rootFile string) error {
	var problems []error
	for _, repoName := range slices.Sorted(maps.Keys(workspace.RepoPaths)) {
		if repoName == workspace.Name || isDeleted(workspace.RepoPaths[repoName]) {
			continue
		}
		worktrees, err := workspace.git.Worktrees(repoName, workspace.RepoPaths[repoName])
		if err != nil {
			problems = append(problems, fmt.Errorf("%s lists the repo %s: %w", rootFile, repoName, err))
			continue
		}
		memberFile := filepath.Join(worktrees[0].Dir, config.FileName)
		// A member with no file declares no app. A file that does not parse is the problem of the stack that reads it.
		memberConfig, err := config.Parse(memberFile)
		if err != nil {
			continue
		}
		suggestedPath, relErr := filepath.Rel(worktrees[0].Dir, workspace.RootDir)
		if relErr != nil {
			suggestedPath = workspace.RootDir
		}
		if memberConfig.MemberOf == "" {
			problems = append(problems, fmt.Errorf("%s lists the repo %s, but %s has no member_of: add member_of: %s there", rootFile, repoName, memberFile, suggestedPath))
			continue
		}
		namedRootDir, err := workspace.git.rootDirOf(memberFile, memberConfig.MemberOf, workspace.HomeDir)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if namedRootDir != workspace.RootDir {
			problems = append(problems, fmt.Errorf("%s lists the repo %s, but %s names another root repo, %s: a repo is in one workspace", rootFile, repoName, memberFile, namedRootDir))
		}
	}
	return errors.Join(problems...)
}

// pathFrom reads a path of a configuration file: under the home folder with ~/, absolute, or relative to the folder of the file.
func pathFrom(baseDir, path, homeDir string) string {
	if rest, isUnderHome := strings.CutPrefix(path, "~/"); isUnderHome {
		return filepath.Join(homeDir, rest)
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}

// ConfigForStack merges the files of the checkouts of a stack.
// A checkout with no ilot.yaml uses the file of the main checkout, so an untracked file there serves every worktree of a repo where you cannot commit one.
func (workspace Workspace) ConfigForStack(stackName string) (config.Config, error) {
	checkoutByRepo, err := workspace.CheckoutsForStack(stackName)
	if err != nil {
		return config.Config{}, err
	}
	configByRepo := map[string]config.Config{}
	for _, repoName := range slices.Sorted(maps.Keys(checkoutByRepo)) {
		configFile, hasFile := ConfigFileOf(checkoutByRepo[repoName])
		if !hasFile {
			continue
		}
		repoConfig, err := config.Parse(configFile)
		if err != nil {
			return config.Config{}, err
		}
		configByRepo[repoName] = repoConfig
	}
	return config.Merge(configByRepo, workspace.Global.Ports)
}

// ConfigFileOf gives the ilot.yaml that a checkout reads: its own, else the one of the main checkout.
func ConfigFileOf(checkout Worktree) (configFile string, hasFile bool) {
	for _, dir := range []string{checkout.Dir, checkout.MainDir} {
		configFile = filepath.Join(dir, config.FileName)
		if _, err := os.Stat(configFile); err == nil {
			return configFile, true
		}
	}
	return "", false
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

// CheckoutsForStack gives the checkout of each cloned repo for a stack: the worktree that is bound to the stack, else the main checkout.
func (workspace Workspace) CheckoutsForStack(stackName string) (map[string]Worktree, error) {
	checkoutByRepo := map[string]Worktree{}
	for _, repoName := range slices.Sorted(maps.Keys(workspace.RepoPaths)) {
		if isDeleted(workspace.RepoPaths[repoName]) {
			continue
		}
		worktrees, err := workspace.git.Worktrees(repoName, workspace.RepoPaths[repoName])
		if err != nil {
			return nil, err
		}
		checkoutByRepo[repoName] = worktrees[0]
		// A bound worktree whose folder is deleted is not in the list, so the stack is back on the main checkout.
		for _, linkedWorktree := range worktrees[1:] {
			if linkedWorktree.Dir == workspace.Bindings[stackName][repoName] {
				checkoutByRepo[repoName] = linkedWorktree
			}
		}
	}
	return checkoutByRepo, nil
}

// The main checkout tells the repo, so a repo path that is itself a linked worktree works too.
func (workspace Workspace) repoOf(worktree Worktree) (string, error) {
	for _, repoName := range slices.Sorted(maps.Keys(workspace.RepoPaths)) {
		if isDeleted(workspace.RepoPaths[repoName]) {
			continue
		}
		worktrees, err := workspace.git.Worktrees(repoName, workspace.RepoPaths[repoName])
		if err == nil && worktrees[0].Dir == worktree.MainDir {
			return repoName, nil
		}
	}
	return "", fmt.Errorf("%s is in no repo of the workspace %s", worktree.Dir, workspace.Name)
}
