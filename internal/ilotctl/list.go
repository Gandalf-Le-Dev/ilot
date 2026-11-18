package ilotctl

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/checkouts"
)

type Listing struct {
	Apps             []AppRow             `json:"apps"`
	ExternalServices []ExternalServiceRow `json:"external_services"`
}

type AppRow struct {
	Stack  string         `json:"stack"`
	App    string         `json:"app"`
	Status string         `json:"status"`
	Ports  map[string]int `json:"ports"`
	URL    string         `json:"url"`
}

type ExternalServiceRow struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Port   int    `json:"port"`
}

func (controller *Controller) List(ctx context.Context) (Listing, error) {
	allStacks, err := controller.Store.All()
	if err != nil {
		return Listing{}, err
	}
	var stackNames []string
	for _, stackState := range allStacks {
		stackNames = append(stackNames, stackState.Name)
	}
	for _, problem := range controller.loadEveryReadableStackConfig(stackNames) {
		fmt.Fprintf(controller.Output, "warning: %v\n", problem)
	}
	statusByProcess, err := controller.Supervisor.Statuses(ctx)
	if err != nil {
		return Listing{}, err
	}
	listing := Listing{Apps: []AppRow{}, ExternalServices: []ExternalServiceRow{}}
	for _, stackState := range allStacks {
		for _, appName := range slices.Sorted(maps.Keys(stackState.Apps)) {
			appInstance := instance{stackName: stackState.Name, appName: appName}
			status := "stopped"
			if processStatus, isKnown := statusByProcess[appInstance.processName()]; isKnown {
				status = processStatus.Status
			}
			listing.Apps = append(listing.Apps, AppRow{
				Stack:  stackState.Name,
				App:    appName,
				Status: status,
				Ports:  stackState.Apps[appName].Ports,
				URL:    controller.publicURL(appInstance),
			})
		}
	}
	for _, externalName := range slices.Sorted(maps.Keys(controller.Config.External)) {
		port := controller.Config.External[externalName].Port
		status := "down"
		if controller.IsExternalServiceUp(port) {
			status = "up"
		}
		listing.ExternalServices = append(listing.ExternalServices, ExternalServiceRow{Name: externalName, Status: status, Port: port})
	}
	return listing, nil
}

// A repo with several worktrees for a stack is left out: ilot start reports it in that stack.
func (controller *Controller) StackCheckouts() (map[string]map[string]checkouts.Checkout, error) {
	allStacks, err := controller.Store.All()
	if err != nil {
		return nil, err
	}
	checkoutsByStack := map[string]map[string]checkouts.Checkout{}
	for _, stackState := range allStacks {
		checkoutByRepo, err := controller.CheckoutsForStack(stackState.Name)
		if err != nil && !checkouts.IsSeveralWorktrees(err) {
			return nil, err
		}
		checkoutsByStack[stackState.Name] = checkoutByRepo
	}
	return checkoutsByStack, nil
}

func (controller *Controller) publicURL(appInstance instance) string {
	if controller.app(appInstance).Route == "" {
		return ""
	}
	return controller.publicBaseURL(appInstance) + controller.app(appInstance).HomePath
}

func (controller *Controller) publicBaseURL(appInstance instance) string {
	url := fmt.Sprintf("http://%s.%s.%s", appInstance.appName, appInstance.stackName, controller.Config.Domain)
	if controller.ProxyPort != "" {
		url += ":" + controller.ProxyPort
	}
	return url
}

func (controller *Controller) printURLs(stacks stackSet, requestedInstances []instance) {
	for _, appInstance := range requestedInstances {
		if url := controller.publicURL(appInstance); url != "" && stacks.isRunning(appInstance) {
			fmt.Fprintf(controller.Output, "%s: %s\n", appInstance.processName(), url)
		}
	}
}

func (controller *Controller) HomeURL() string {
	url := "http://home." + controller.Config.Domain
	if controller.ProxyPort != "" {
		url += ":" + controller.ProxyPort
	}
	return url
}
