package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
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
	// A check must work where the state cannot be read, and then the branch names the stack.
	if boundWorkspace, _, err := withBindings(currentWorkspace); err == nil {
		currentWorkspace = boundWorkspace
	}
	checkedFile, otherFileByRepo, err := configFilesToCheck(currentWorkspace, checkout, requestedFile)
	if err != nil {
		return err
	}
	problems, warnings := checkConfigFile(currentWorkspace.Name, slices.Sorted(maps.Keys(currentWorkspace.RepoPaths)), currentWorkspace.Global.Ports, checkout.Repo, checkedFile, otherFileByRepo)
	warnings = append(warnings, unknownAppsOfTheGlobalFile(currentWorkspace.Name, currentWorkspace.Settings, checkedFile, otherFileByRepo)...)
	for _, warning := range warnings {
		fmt.Fprintln(os.Stderr, "warning: "+warning)
	}
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

func checkConfigFile(workspaceName string, repoNames []string, allocatedPorts config.PortRange, checkedRepo, checkedFile string, otherFileByRepo map[string]string) (problems []error, warnings []string) {
	fileByRepo := map[string]string{checkedRepo: checkedFile}
	for repoName, configFile := range otherFileByRepo {
		if repoName != checkedRepo {
			fileByRepo[repoName] = configFile
		}
	}
	configByRepo := map[string]config.Config{}
	for _, repoName := range slices.Sorted(maps.Keys(fileByRepo)) {
		repoConfig, err := config.Parse(fileByRepo[repoName])
		if err != nil {
			problems = append(problems, err)
			continue
		}
		configByRepo[repoName] = repoConfig
	}
	if len(problems) > 0 {
		return problems, nil
	}
	merged, err := config.Merge(configByRepo, allocatedPorts)
	if err != nil {
		mergedFiles := slices.Sorted(maps.Values(fileByRepo))
		problems = append(problems, fmt.Errorf("%s: %w", strings.Join(mergedFiles, " + "), err))
	}
	for _, problem := range plan.CheckRendering(workspaceName, repoNames, merged) {
		if renderProblem, isRenderProblem := errors.AsType[plan.RenderProblem](problem); isRenderProblem {
			problem = fmt.Errorf("%s: %w", fileByRepo[renderProblem.Repo], problem)
		}
		problems = append(problems, problem)
	}
	return problems, plan.CheckWarnings(workspaceName, merged)
}

// The global file can name an app that only another stack declares, so an unknown app is a warning and not a problem.
func unknownAppsOfTheGlobalFile(workspaceName string, settings config.WorkspaceSettings, checkedFile string, otherFileByRepo map[string]string) []string {
	isDeclared := map[string]bool{}
	for _, configFile := range append(slices.Collect(maps.Values(otherFileByRepo)), checkedFile) {
		repoConfig, err := config.Parse(configFile)
		if err != nil {
			return nil
		}
		for appName := range repoConfig.Apps {
			isDeclared[appName] = true
		}
	}
	var warnings []string
	for _, appName := range slices.Sorted(maps.Keys(settings.Apps)) {
		if !isDeclared[appName] {
			warnings = append(warnings, fmt.Sprintf("%s: workspaces.%s.apps.%s gives env to an app that no %s of this stack declares", config.GlobalFileShown(), workspaceName, appName, config.FileName))
		}
	}
	return warnings
}

// configFilesToCheck gives the file to check and the files that the stack of its checkout merges it with.
// With no requested file, the checked file is the one that the checkout reads: its own, else the one of the main checkout.
func configFilesToCheck(currentWorkspace workspace.Workspace, checkout workspace.Worktree, requestedFile string) (checkedFile string, otherFileByRepo map[string]string, err error) {
	checkedFile = requestedFile
	if checkedFile == "" {
		ownFile, hasFile := workspace.ConfigFileOf(checkout)
		if !hasFile {
			return "", nil, fmt.Errorf("the repo %s has no %s in %s", checkout.Repo, config.FileName, checkout.Dir)
		}
		checkedFile = ownFile
	}
	stackName, _, err := currentWorkspace.StackOf(checkout)
	if err != nil {
		return "", nil, err
	}
	checkoutByRepo, err := currentWorkspace.CheckoutsForStack(stackName)
	if err != nil {
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
