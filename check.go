package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/appconfig"
	"github.com/Gandalf-Le-Dev/ilot/internal/ilotctl"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

type checkCommand struct {
	Path string `arg:"" optional:"" help:"ilot.yaml to check. The default is the ilot.yaml of the repo of the current folder."`
}

func (command *checkCommand) Run() error {
	checkedDir, err := os.Getwd()
	if err != nil {
		return err
	}
	requestedFile := ""
	if command.Path != "" {
		if requestedFile, err = workspace.Canonical(command.Path); err != nil {
			return err
		}
		checkedDir = filepath.Dir(requestedFile)
	}
	currentWorkspace, checkout, err := findWorkspaceOf(checkedDir)
	if err != nil {
		return err
	}
	checkedFile, otherFileByRepo, err := configFilesToCheck(currentWorkspace, checkout, requestedFile)
	if err != nil {
		return err
	}
	problems := checkConfigFile(currentWorkspace.Name, slices.Sorted(maps.Keys(currentWorkspace.RepoPaths)), checkout.Repo, checkedFile, otherFileByRepo)
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

func checkConfigFile(workspaceName string, repoNames []string, checkedRepo, checkedFile string, otherFileByRepo map[string]string) []error {
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
	merged, err := appconfig.Merge(workspaceName, configByRepo)
	if err != nil {
		mergedFiles := slices.Sorted(maps.Values(fileByRepo))
		problems = append(problems, fmt.Errorf("%s: %w", strings.Join(mergedFiles, " + "), err))
	}
	for _, problem := range ilotctl.CheckRendering(workspaceName, repoNames, merged) {
		var renderProblem ilotctl.RenderProblem
		if errors.As(problem, &renderProblem) {
			problem = fmt.Errorf("%s: %w", fileByRepo[renderProblem.Repo], problem)
		}
		problems = append(problems, problem)
	}
	return problems
}

// configFilesToCheck gives the file to check and the files that the stack of its checkout merges it with.
// With no requested file, the checked file is the one that the checkout reads: its own, else the one of the main checkout.
func configFilesToCheck(currentWorkspace workspace.Workspace, checkout workspace.Worktree, requestedFile string) (checkedFile string, otherFileByRepo map[string]string, err error) {
	checkedFile = requestedFile
	if checkedFile == "" {
		ownFile, hasFile := workspace.ConfigFileOf(checkout)
		if !hasFile {
			return "", nil, fmt.Errorf("the repo %s has no %s in %s", checkout.Repo, appconfig.FileName, checkout.Dir)
		}
		checkedFile = ownFile
	}
	stackName, err := workspace.StackName(checkout)
	if err != nil {
		return "", nil, err
	}
	checkoutByRepo, err := currentWorkspace.CheckoutsForStack(stackName)
	if err != nil && !workspace.IsSeveralWorktrees(err) {
		return "", nil, err
	}
	otherFileByRepo = map[string]string{}
	for repoName, otherCheckout := range checkoutByRepo {
		if repoName == checkout.Repo {
			continue
		}
		if otherFile, hasFile := workspace.ConfigFileOf(otherCheckout); hasFile {
			otherFileByRepo[repoName] = otherFile
		}
	}
	return checkedFile, otherFileByRepo, nil
}
