package devctl

import (
	"maps"
	"slices"
	"strings"

	"dev/internal/processcompose"
)

const containerNetwork = "devproxy"

func containerName(appInstance instance) string {
	return "dev-" + appInstance.stackName + "-" + appInstance.appName
}

func (controller *Controller) isContainer(appInstance instance) bool {
	return !controller.app(appInstance).Container.IsZero()
}

// The leftover of a crash or a killed dev holds the name, so the command removes it before running.
func (controller *Controller) containerCommand(stacks stackSet, appInstance instance, envEntries []EnvEntry) (string, error) {
	container := controller.app(appInstance).Container
	name := processcompose.ShellQuote(containerName(appInstance))
	commandParts := []string{"docker rm -f " + name + " >/dev/null 2>&1 || true && exec docker run --rm --name " + name + " --network " + containerNetwork}
	for _, envFile := range container.EnvFiles {
		commandParts = append(commandParts, "--env-file "+processcompose.ShellQuote(envFile))
	}
	for _, entry := range envEntries {
		commandParts = append(commandParts, "-e "+processcompose.ShellQuote(entry.Key))
	}
	for _, containerPath := range slices.Sorted(maps.Keys(container.Mounts)) {
		source, err := controller.renderTemplate(stacks, appInstance, container.Mounts[containerPath])
		if err != nil {
			return "", err
		}
		commandParts = append(commandParts, "-v "+processcompose.ShellQuote(controller.HostPath(source)+":"+containerPath))
	}
	for _, alias := range slices.Sorted(maps.Keys(container.Links)) {
		linkedInstance := instance{stackName: appInstance.stackName, appName: container.Links[alias]}
		commandParts = append(commandParts, "--link "+processcompose.ShellQuote(containerName(linkedInstance)+":"+alias))
	}
	for _, alias := range slices.Sorted(maps.Keys(container.ExternalLinks)) {
		target, err := controller.renderTemplate(stacks, appInstance, container.ExternalLinks[alias])
		if err != nil {
			return "", err
		}
		commandParts = append(commandParts, "--link "+processcompose.ShellQuote(target+":"+alias))
	}
	commandParts = append(commandParts, processcompose.ShellQuote(container.Image))
	return strings.Join(commandParts, " "), nil
}

// docker run only forwards SIGTERM, and Postgres answers it by waiting for its clients: docker stop kills after 10 s.
func (controller *Controller) shutdownCommand(appInstance instance) string {
	if !controller.isContainer(appInstance) {
		return ""
	}
	return "docker stop --time 10 " + processcompose.ShellQuote(containerName(appInstance))
}

// Personal top-level env is for processes: it can hold tokens that third-party images have no business reading. The shell is the daemon's, not the image's.
func containerEnvEntries(envEntries []EnvEntry) []EnvEntry {
	return slices.DeleteFunc(slices.Clone(envEntries), func(entry EnvEntry) bool {
		return entry.Source == sourceShell || entry.Source == sourceDefault || entry.Source == sourcePersonal
	})
}

func (controller *Controller) readyProbe(stacks stackSet, appInstance instance) processcompose.ReadyProbe {
	app := controller.app(appInstance)
	probe := processcompose.ReadyProbe{Host: "", Port: stacks.app(appInstance).Ports[readyPortName(app)], Path: app.Ready.Path, Command: app.Ready.Command}
	if !controller.isContainer(appInstance) {
		return probe
	}
	probe.Host = containerName(appInstance)
	if app.Ready.Command != "" {
		probe.Command = "docker exec " + processcompose.ShellQuote(containerName(appInstance)) + " sh -c " + processcompose.ShellQuote(app.Ready.Command)
	}
	return probe
}

func (controller *Controller) isLinkedContainer(appInstance instance, dependencyName string) bool {
	return slices.Contains(slices.Collect(maps.Values(controller.app(appInstance).Container.Links)), dependencyName)
}

func (controller *Controller) routeHost(appInstance instance) string {
	if controller.isContainer(appInstance) {
		return containerName(appInstance)
	}
	return controller.BackendHost
}

// How a caller reaches an app: a container by its name, a process from a container through devbox's name, a process from a process on localhost.
func (controller *Controller) hostFor(callerInstance, targetInstance instance) string {
	switch {
	case controller.isContainer(targetInstance):
		return containerName(targetInstance)
	case controller.isContainer(callerInstance):
		return controller.BackendHost
	}
	return "localhost"
}
