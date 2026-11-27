package ilotctl

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

func (controller *Controller) checkExternalServicesUp(externalNames []string) error {
	var problems []error
	for _, externalName := range externalNames {
		externalService := controller.Config.External[externalName]
		if !controller.IsExternalServiceUp(externalService.Port) {
			problems = append(problems, fmt.Errorf("external service %s is down on port %d, start it with: %s", externalName, externalService.Port, externalService.StartHint))
		}
	}
	return errors.Join(problems...)
}

// checkStoredPorts tests each port that an instance to start already has: its fixed ports, and the ports of an earlier start.
// Another program can have taken one since, and the app would then fail with an error of its own.
func (controller *Controller) checkStoredPorts(before plan.Snapshot, start plan.Start, processByName map[string]daemon.Process) error {
	fixedPortsOf := map[plan.Instance]map[string]int{}
	for _, fixedPort := range start.FixedPorts {
		if fixedPortsOf[fixedPort.Instance] == nil {
			fixedPortsOf[fixedPort.Instance] = map[string]int{}
		}
		fixedPortsOf[fixedPort.Instance][fixedPort.PortName] = fixedPort.Port
	}
	for _, appInstance := range start.Order {
		if process, isHeld := processByName[before.ProcessName(appInstance)]; isHeld && process.State != daemon.StateExited {
			continue
		}
		ports := maps.Clone(before.Ports(appInstance))
		maps.Copy(ports, fixedPortsOf[appInstance])
		for _, portName := range slices.Sorted(maps.Keys(ports)) {
			if !controller.IsPortFreeOnHost(ports[portName]) {
				return fmt.Errorf("port %d (%s) of %s is taken by another process: stop that process, then run the command again", ports[portName], portName, before.ProcessName(appInstance))
			}
		}
	}
	return nil
}

// allocatePorts gives each instance of the start, in the state after it, a free port for each port that has no number yet.
func (controller *Controller) allocatePorts(start plan.Start) error {
	allocated, err := controller.PortRange.Allocate(len(start.PortNeeds), start.After.State.PortsInUse(), controller.IsPortFreeOnHost)
	if err != nil {
		return err
	}
	for index, need := range start.PortNeeds {
		start.After.SetPort(need.Instance, need.PortName, allocated[index])
	}
	return nil
}

func (controller *Controller) buildApps(ctx context.Context, snapshot plan.Snapshot, startOrder []plan.Instance, needsBuild func(plan.Instance) bool) error {
	type buildInWorktree struct {
		workingDir string
		command    string
	}
	isBuilt := map[buildInWorktree]bool{}
	for _, appInstance := range startOrder {
		if err := ctx.Err(); err != nil {
			return err
		}
		app := snapshot.App(appInstance)
		if app.Build == "" || !needsBuild(appInstance) {
			continue
		}
		buildCommand, err := snapshot.Render(appInstance, app.Build)
		if err != nil {
			return err
		}
		workingDir, err := snapshot.CheckoutDir(appInstance)
		if err != nil {
			return err
		}
		build := buildInWorktree{workingDir: workingDir, command: snapshot.Wrapped(appInstance, buildCommand)}
		if isBuilt[build] {
			continue
		}
		isBuilt[build] = true
		if err := os.MkdirAll(snapshot.BinDir(appInstance.Stack), 0o755); err != nil {
			return err
		}
		releaseBuilds, err := controller.lockBuilds(ctx, appInstance.Stack)
		if err != nil {
			return err
		}
		fmt.Fprintf(controller.Output, "building %s in %s\n", plan.ProcessName(controller.Workspace, appInstance), workingDir)
		stopProgress := controller.printProgressUntilStopped("building " + plan.ProcessName(controller.Workspace, appInstance))
		err = controller.RunBuild(ctx, workingDir, build.command, buildEnviron(snapshot.Machine))
		stopProgress()
		if err := errors.Join(err, releaseBuilds()); err != nil {
			return fmt.Errorf("build %s: %w", plan.ProcessName(controller.Workspace, appInstance), err)
		}
	}
	return nil
}

const buildLockPollInterval = 100 * time.Millisecond

// lockBuilds lets one build run in a stack at a time: two apps of a stack can write the same binary.
// ilot start, ilot restart and the watcher of an app all build through it.
func (controller *Controller) lockBuilds(ctx context.Context, stackName string) (release func() error, err error) {
	if err := os.MkdirAll(controller.StackDir(stackName), 0o700); err != nil {
		return nil, err
	}
	lockFile, err := os.OpenFile(filepath.Join(controller.StackDir(stackName), "build.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	hasSaidThatItWaits := false
	for {
		err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return lockFile.Close, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.Join(fmt.Errorf("lock %s: %w", lockFile.Name(), err), lockFile.Close())
		}
		if !hasSaidThatItWaits {
			fmt.Fprintf(controller.Output, "waiting for another build of stack %s\n", stackName)
			hasSaidThatItWaits = true
		}
		// A poll, and not a blocking lock, so that Ctrl-C ends the wait.
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), lockFile.Close())
		case <-time.After(buildLockPollInterval):
		}
	}
}

func (controller *Controller) loadState() (state.State, error) {
	return controller.Store.Load()
}

// loadStacks gives the snapshot of a command of a stack, and the apps that its config no longer declares, which the command stops.
func (controller *Controller) loadStacks(stackName string) (plan.Snapshot, []plan.Instance, error) {
	snapshot, dropped, err := controller.Snapshot(stackName)
	if err != nil {
		return plan.Snapshot{}, nil, err
	}
	controller.warnAboutUndeclared(snapshot, dropped)
	return snapshot, dropped, nil
}

func (controller *Controller) warnAboutUndeclared(snapshot plan.Snapshot, dropped []plan.Instance) {
	for _, droppedInstance := range dropped {
		fmt.Fprintf(controller.Output, "warning: %s stopped: no ilot.yaml of stack %s declares it any more\n", snapshot.ProcessName(droppedInstance), droppedInstance.Stack)
	}
}

func (controller *Controller) saveStacks(snapshot plan.Snapshot) error {
	if err := snapshot.State.Claim(controller.Workspace, controller.Root); err != nil {
		return err
	}
	return controller.Store.Save(snapshot.State)
}

// A build runs in the terminal of the command, so it gets the session variables too, for example SSH_AUTH_SOCK for a private module.
func buildEnviron(machine plan.Machine) []string {
	environment := maps.Clone(machine.SessionEnv)
	if environment == nil {
		environment = map[string]string{}
	}
	maps.Copy(environment, machine.ShellEnv)
	return environ(environment)
}
