package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

type appsCommand struct {
	JSON bool `help:"Print the apps and the external services as one JSON object, for tools and agents."`
}

type declaredServices struct {
	Stack            string                    `json:"stack"`
	Apps             []declaredApp             `json:"apps"`
	ExternalServices []declaredExternalService `json:"external_services"`
}

type declaredApp struct {
	Name      string   `json:"name"`
	Repo      string   `json:"repo"`
	DependsOn []string `json:"depends_on"`
}

type declaredExternalService struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

func (command *appsCommand) Run() error {
	currentWorkspace, worktree, err := findCurrentWorkspace()
	if err != nil {
		return err
	}
	stackName, err := workspace.StackName(worktree)
	if err != nil {
		return err
	}
	config, err := currentWorkspace.ConfigForStack(stackName)
	if err != nil {
		return err
	}
	services := declaredServicesOf(stackName, config)
	if command.JSON {
		return json.NewEncoder(os.Stdout).Encode(services)
	}
	return printDeclaredServices(os.Stdout, services)
}

func declaredServicesOf(stackName string, config config.Config) declaredServices {
	services := declaredServices{Stack: stackName, Apps: []declaredApp{}, ExternalServices: []declaredExternalService{}}
	for _, appName := range slices.Sorted(maps.Keys(config.Apps)) {
		app := config.Apps[appName]
		services.Apps = append(services.Apps, declaredApp{Name: appName, Repo: app.Repo, DependsOn: append([]string{}, app.DependsOn...)})
	}
	for _, externalName := range slices.Sorted(maps.Keys(config.External)) {
		services.ExternalServices = append(services.ExternalServices, declaredExternalService{Name: externalName, Port: config.External[externalName].Port})
	}
	return services
}

func printDeclaredServices(output io.Writer, services declaredServices) error {
	fmt.Fprintf(output, "stack: %s\n\n", services.Stack)
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "APP\tREPO\tDEPENDS ON")
	for _, app := range services.Apps {
		fmt.Fprintf(table, "%s\t%s\t%s\n", app.Name, app.Repo, strings.Join(app.DependsOn, ", "))
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if len(services.ExternalServices) == 0 {
		return nil
	}
	fmt.Fprintln(output)
	fmt.Fprintln(table, "EXTERNAL\tPORT")
	for _, externalService := range services.ExternalServices {
		fmt.Fprintf(table, "%s\t%d\n", externalService.Name, externalService.Port)
	}
	return table.Flush()
}
