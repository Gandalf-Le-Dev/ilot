package main

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"dev/internal/appconfig"
	"dev/internal/checkouts"
	"dev/internal/devctl"
	"dev/internal/stack"
)

type checkCommand struct {
	Path string `arg:"" optional:"" help:"dev.yaml to check. The default is the dev.yaml of the repo of the current folder."`
}

func (command *checkCommand) Run() error {
	currentDir, err := os.Getwd()
	if err != nil {
		return err
	}
	var currentWorkspace *workspace
	loadedWorkspace, err := loadCurrentWorkspace()
	switch {
	case err == nil:
		currentWorkspace = &loadedWorkspace
	case command.Path == "":
		return err
	}
	checkedRepo, checkedFile, err := configFileToCheck(currentWorkspace, currentDir, command.Path)
	if err != nil {
		return err
	}
	otherFileByRepo := map[string]string{}
	if currentWorkspace != nil {
		otherFileByRepo, err = otherConfigFiles(*currentWorkspace, currentDir, checkedRepo)
		if err != nil {
			return err
		}
	}
	problems := checkConfigFile(checkedRepo, checkedFile, otherFileByRepo)
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, problem)
	}
	if len(problems) == 1 {
		return errors.New("1 problem found")
	}
	if len(problems) > 1 {
		return fmt.Errorf("%d problems found", len(problems))
	}
	fmt.Printf("%s: ok\n", checkedFile)
	return nil
}

func checkConfigFile(checkedRepo, checkedFile string, otherFileByRepo map[string]string) []error {
	fileByRepo := map[string]string{checkedRepo: checkedFile}
	for repoName, configFile := range otherFileByRepo {
		if repoName != checkedRepo {
			fileByRepo[repoName] = configFile
		}
	}
	configByRepo := map[string]appconfig.Config{}
	var problems []error
	for _, repoName := range slices.Sorted(maps.Keys(fileByRepo)) {
		config, err := appconfig.Parse(fileByRepo[repoName])
		if err != nil {
			problems = append(problems, err)
			continue
		}
		configByRepo[repoName] = config
	}
	if len(problems) > 0 {
		return problems
	}
	merged, err := appconfig.Merge(configByRepo)
	if err != nil {
		mergedFiles := slices.Sorted(maps.Values(fileByRepo))
		problems = append(problems, fmt.Errorf("%s: %w", strings.Join(mergedFiles, " + "), err))
	}
	for _, problem := range devctl.CheckRendering(merged) {
		var renderProblem devctl.RenderProblem
		if errors.As(problem, &renderProblem) {
			problem = fmt.Errorf("%s: %w", fileByRepo[renderProblem.Repo], problem)
		}
		problems = append(problems, problem)
	}
	return problems
}

// A nil workspace is CI: no repo layout, so a file is known only by its own repos: list.
func configFileToCheck(currentWorkspace *workspace, currentDir, requestedPath string) (repoName, configFile string, err error) {
	if requestedPath != "" {
		return repoOfConfigFile(currentWorkspace, requestedPath)
	}
	if currentWorkspace.locationOf(currentDir) != insideCheckout {
		mainCheckout, err := platformMainCheckout(*currentWorkspace)
		if err != nil {
			return "", "", err
		}
		return appconfig.DefaultRepo, filepath.Join(mainCheckout.WorktreeDir, platformConfigPath), nil
	}
	repoName, err = checkouts.RepoOf(currentWorkspace.repoPaths, currentDir)
	if err != nil {
		return "", "", err
	}
	checkout, err := stack.Locate(currentDir)
	if err != nil {
		return "", "", err
	}
	relativeConfigPath := currentWorkspace.repoConfigs[repoName].Config
	if relativeConfigPath == "" {
		return "", "", fmt.Errorf("repo %s has no config file in the repos: list of %s", repoName, platformConfigPath)
	}
	return repoName, filepath.Join(checkout.WorktreeDir, relativeConfigPath), nil
}

func repoOfConfigFile(currentWorkspace *workspace, requestedPath string) (repoName, configFile string, err error) {
	configFile, err = filepath.Abs(requestedPath)
	if err != nil {
		return "", "", err
	}
	if currentWorkspace != nil {
		if repoName, err := checkouts.RepoOf(currentWorkspace.repoPaths, filepath.Dir(configFile)); err == nil {
			return repoName, configFile, nil
		}
	}
	config, err := appconfig.Parse(configFile)
	if err != nil {
		return "", "", err
	}
	if len(config.Repos) == 0 {
		return "", "", fmt.Errorf("%s is in no checkout that dev knows and has no repos: list, so dev cannot tell its repo", configFile)
	}
	return appconfig.DefaultRepo, configFile, nil
}

func otherConfigFiles(currentWorkspace workspace, currentDir, checkedRepo string) (map[string]string, error) {
	stackName := stack.MasterStackName
	if currentWorkspace.locationOf(currentDir) == insideCheckout {
		if _, err := checkouts.RepoOf(currentWorkspace.repoPaths, currentDir); err == nil {
			checkout, err := stack.Locate(currentDir)
			if err != nil {
				return nil, err
			}
			stackName = checkout.Name
		}
	}
	checkoutByRepo, err := currentWorkspace.checkoutsForStack(stackName)
	if err != nil && !checkouts.IsSeveralWorktrees(err) {
		return nil, err
	}
	fileByRepo := map[string]string{}
	for repoName, checkout := range checkoutByRepo {
		relativeConfigPath := currentWorkspace.repoConfigs[repoName].Config
		if repoName == checkedRepo || relativeConfigPath == "" {
			continue
		}
		configFile := filepath.Join(checkout.Dir, relativeConfigPath)
		if _, err := os.Stat(configFile); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		fileByRepo[repoName] = configFile
	}
	return fileByRepo, nil
}
