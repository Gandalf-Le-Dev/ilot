package ilotctl

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
)

// The name that Docker gives to this machine, as a container sees it.
const hostNameInContainer = "host.docker.internal"

// The workspace is in the name, because two workspaces can have an app of one name in the base stack.
// An underscore separates the parts, because a name of an app, a stack or a workspace can hold a dash and never an underscore.
func (controller *Controller) containerName(appInstance plan.Instance) string {
	return "ilot_" + appInstance.App + "_" + appInstance.Stack + "_" + controller.Workspace
}

func (controller *Controller) isContainer(snapshot plan.Snapshot, appInstance plan.Instance) bool {
	return !snapshot.App(appInstance).Container.IsZero()
}

// The leftover of a crash or a killed ilot holds the name, so the command removes it before running.
// Each port is published on the loopback address, so the proxy and the other apps reach a container as they reach a process.
func (controller *Controller) containerCommand(snapshot plan.Snapshot, appInstance plan.Instance, envEntries []EnvEntry) (string, error) {
	container := snapshot.App(appInstance).Container
	name := shellQuote(controller.containerName(appInstance))
	commandParts := []string{
		"docker rm -f " + name + " >/dev/null 2>&1 || true && exec docker run --rm --name " + name,
		// Docker Desktop knows this name. Docker on Linux needs the flag.
		"--add-host " + shellQuote(hostNameInContainer+":host-gateway"),
	}
	stackPorts := snapshot.Ports(appInstance)
	for _, portName := range slices.Sorted(maps.Keys(container.Ports)) {
		if stackPorts[portName] == 0 {
			return "", fmt.Errorf("%s has no port for %q yet", controller.processName(appInstance), portName)
		}
		commandParts = append(commandParts, fmt.Sprintf("-p 127.0.0.1:%d:%d", stackPorts[portName], container.Ports[portName]))
	}
	for _, envFile := range container.EnvFiles {
		commandParts = append(commandParts, "--env-file "+shellQuote(envFile))
	}
	for _, entry := range envEntries {
		commandParts = append(commandParts, "-e "+shellQuote(entry.Key))
	}
	for _, sourceTemplate := range slices.Sorted(maps.Keys(container.Mounts)) {
		source, err := controller.renderTemplate(snapshot, appInstance, sourceTemplate)
		if err != nil {
			return "", err
		}
		commandParts = append(commandParts, "-v "+shellQuote(source+":"+container.Mounts[sourceTemplate]))
	}
	for _, argumentTemplate := range container.Args {
		argument, err := controller.renderTemplate(snapshot, appInstance, argumentTemplate)
		if err != nil {
			return "", err
		}
		commandParts = append(commandParts, shellQuote(argument))
	}
	commandParts = append(commandParts, shellQuote(container.Image))
	return strings.Join(commandParts, " "), nil
}

// docker run only forwards SIGTERM, and Postgres answers it by waiting for its clients: docker stop kills after 10 s.
func (controller *Controller) containerStopCommand(appInstance plan.Instance) string {
	return "docker stop --time 10 " + shellQuote(controller.containerName(appInstance))
}

// The env of the workspace in the global file is for processes: it can hold tokens that third-party images have no business reading. The shell is for the docker command, not for the image.
func containerEnvEntries(envEntries []EnvEntry) []EnvEntry {
	return slices.DeleteFunc(slices.Clone(envEntries), func(entry EnvEntry) bool {
		return entry.Source == sourceShell || entry.Source == sourceGlobal
	})
}

// With no probe, the daemon calls a process ready when it stays alive.
func (controller *Controller) readyProbe(snapshot plan.Snapshot, appInstance plan.Instance) *daemon.Probe {
	app := snapshot.App(appInstance)
	probe := daemon.Probe{Host: "", Port: snapshot.Ports(appInstance)[readyPortName(app)], Path: app.Ready.Path, Command: app.Ready.Command}
	if controller.isContainer(snapshot, appInstance) && app.Ready.Command != "" {
		probe.Command = "docker exec " + shellQuote(controller.containerName(appInstance)) + " sh -c " + shellQuote(app.Ready.Command)
	}
	if probe.Validate() != nil {
		return nil
	}
	return &probe
}

// Every app listens on a loopback port of this machine. A process reaches it on localhost, and a container through the name of the machine.
func (controller *Controller) hostFor(snapshot plan.Snapshot, callerInstance plan.Instance) string {
	if controller.isContainer(snapshot, callerInstance) {
		return hostNameInContainer
	}
	return "localhost"
}
