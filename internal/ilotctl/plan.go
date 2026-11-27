package ilotctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

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

// allocatePorts gives each instance of the start its fixed ports and a free port for each port that has no number yet.
func (controller *Controller) allocatePorts(snapshot plan.Snapshot, start plan.Start) error {
	for _, fixedPort := range start.FixedPorts {
		if !snapshot.IsWanted(fixedPort.Instance) && !controller.IsPortFreeOnHost(fixedPort.Port) {
			return fmt.Errorf("port %d of %s is taken by another process: stop it first (supervisor?)", fixedPort.Port, plan.ProcessName(controller.Workspace, fixedPort.Instance))
		}
		snapshot.SetPort(fixedPort.Instance, fixedPort.PortName, fixedPort.Port)
	}
	allocated, err := controller.PortRange.Allocate(len(start.PortNeeds), snapshot.State.PortsInUse(), controller.IsPortFreeOnHost)
	if err != nil {
		return err
	}
	for index, need := range start.PortNeeds {
		snapshot.SetPort(need.Instance, need.PortName, allocated[index])
	}
	return nil
}

func (controller *Controller) checkEveryProcessRenders(snapshot plan.Snapshot, startOrder []plan.Instance) error {
	stacksWithPlanRunning := snapshot.Clone()
	for _, appInstance := range startOrder {
		stacksWithPlanRunning.SetWanted(appInstance, true)
	}
	for _, appInstance := range startOrder {
		if _, err := stacksWithPlanRunning.Spec(appInstance); err != nil {
			return err
		}
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
		err = controller.RunBuild(ctx, workingDir, build.command, environ(snapshot.Machine.ShellEnv))
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

// loadStacks gives the snapshot of a command of a stack, and warns about each app that no config declares any more.
func (controller *Controller) loadStacks(stackName string) (plan.Snapshot, error) {
	snapshot, dropped, err := controller.Snapshot(stackName)
	controller.warnAboutUndeclared(snapshot, dropped)
	return snapshot, err
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
