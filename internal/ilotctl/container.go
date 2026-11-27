package ilotctl

import (
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"maps"
	"slices"
	"strings"
)

const containerNetwork = "ilotproxy"

func containerName(appInstance instance) string {
	return "ilot-" + appInstance.stackName + "-" + appInstance.appName
}

func (controller *Controller) isContainer(appInstance instance) bool {
	return !controller.app(appInstance).Container.IsZero()
}

// The leftover of a crash or a killed ilot holds the name, so the command removes it before running.
func (controller *Controller) containerCommand(stacks stackSet, appInstance instance, envEntries []EnvEntry) (string, error) {
	container := controller.app(appInstance).Container
	name := shellQuote(containerName(appInstance))
	commandParts := []string{"docker rm -f " + name + " >/dev/null 2>&1 || true && exec docker run --rm --name " + name + " --network " + containerNetwork}
	for _, envFile := range container.EnvFiles {
		commandParts = append(commandParts, "--env-file "+shellQuote(envFile))
	}
	for _, entry := range envEntries {
		commandParts = append(commandParts, "-e "+shellQuote(entry.Key))
	}
	for _, containerPath := range slices.Sorted(maps.Keys(container.Mounts)) {
		source, err := controller.renderTemplate(stacks, appInstance, container.Mounts[containerPath])
		if err != nil {
			return "", err
		}
		commandParts = append(commandParts, "-v "+shellQuote(source+":"+containerPath))
	}
	for _, alias := range slices.Sorted(maps.Keys(container.Links)) {
		linkedInstance := instance{stackName: appInstance.stackName, appName: container.Links[alias]}
		commandParts = append(commandParts, "--link "+shellQuote(containerName(linkedInstance)+":"+alias))
	}
	for _, alias := range slices.Sorted(maps.Keys(container.ExternalLinks)) {
		target, err := controller.renderTemplate(stacks, appInstance, container.ExternalLinks[alias])
		if err != nil {
			return "", err
		}
		commandParts = append(commandParts, "--link "+shellQuote(target+":"+alias))
	}
	commandParts = append(commandParts, shellQuote(container.Image))
	return strings.Join(commandParts, " "), nil
}

// docker run only forwards SIGTERM, and Postgres answers it by waiting for its clients: docker stop kills after 10 s.
func containerStopCommand(appInstance instance) string {
	return "docker stop --time 10 " + shellQuote(containerName(appInstance))
}

// Personal top-level env is for processes: it can hold tokens that third-party images have no business reading. The shell is for the docker command, not for the image.
func containerEnvEntries(envEntries []EnvEntry) []EnvEntry {
	return slices.DeleteFunc(slices.Clone(envEntries), func(entry EnvEntry) bool {
		return entry.Source == sourceShell || entry.Source == sourcePersonal
	})
}

// With no probe, the daemon calls a process ready when it stays alive.
func (controller *Controller) readyProbe(stacks stackSet, appInstance instance) *daemon.Probe {
	app := controller.app(appInstance)
	probe := daemon.Probe{Host: "", Port: stacks.app(appInstance).Ports[readyPortName(app)], Path: app.Ready.Path, Command: app.Ready.Command}
	if controller.isContainer(appInstance) {
		probe.Host = containerName(appInstance)
		if app.Ready.Command != "" {
			probe.Command = "docker exec " + shellQuote(containerName(appInstance)) + " sh -c " + shellQuote(app.Ready.Command)
		}
	}
	if probe.Validate() != nil {
		return nil
	}
	return &probe
}

func (controller *Controller) isLinkedContainer(appInstance instance, dependencyName string) bool {
	return slices.Contains(slices.Collect(maps.Values(controller.app(appInstance).Container.Links)), dependencyName)
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
