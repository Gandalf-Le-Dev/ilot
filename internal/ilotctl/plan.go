package ilotctl

import (
	"cmp"
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
			return fmt.Errorf("port %d of %s is taken by another process: stop it first (supervisor?)", fixedPort.Port, controller.processName(fixedPort.Instance))
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
		if _, err := controller.processSpec(stacksWithPlanRunning, appInstance); err != nil {
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
		buildCommand, err := controller.renderTemplate(snapshot, appInstance, app.Build)
		if err != nil {
			return err
		}
		workingDir, err := controller.checkoutDir(snapshot, appInstance)
		if err != nil {
			return err
		}
		build := buildInWorktree{workingDir: workingDir, command: controller.wrapped(snapshot, appInstance, buildCommand)}
		if isBuilt[build] {
			continue
		}
		isBuilt[build] = true
		if err := os.MkdirAll(controller.binDir(appInstance.Stack), 0o755); err != nil {
			return err
		}
		environ, err := controller.shellEnviron()
		if err != nil {
			return err
		}
		releaseBuilds, err := controller.lockBuilds(ctx, appInstance.Stack)
		if err != nil {
			return err
		}
		fmt.Fprintf(controller.Output, "building %s in %s\n", controller.processName(appInstance), workingDir)
		stopProgress := controller.printProgressUntilStopped("building " + controller.processName(appInstance))
		err = controller.RunBuild(ctx, workingDir, build.command, environ)
		stopProgress()
		if err := errors.Join(err, releaseBuilds()); err != nil {
			return fmt.Errorf("build %s: %w", controller.processName(appInstance), err)
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

// loadStacks gives the snapshot of a command of a stack: the state, the configuration of each stack, and the checkouts that the bindings give.
func (controller *Controller) loadStacks(stackName string) (plan.Snapshot, error) {
	current, err := controller.loadState()
	if err != nil {
		return plan.Snapshot{}, err
	}
	current.Stack(controller.Workspace, plan.BaseStack)
	current.Stack(controller.Workspace, stackName)
	snapshot, problems := controller.snapshotOf(current, stackName)
	if err := errors.Join(problems...); err != nil {
		return plan.Snapshot{}, err
	}
	if err := controller.deriveCheckouts(snapshot); err != nil {
		return plan.Snapshot{}, err
	}
	controller.prepareStacks(snapshot)
	return snapshot, nil
}

// A bound worktree that is deleted gives its repo back to the main checkout.
func (controller *Controller) deriveCheckouts(snapshot plan.Snapshot) error {
	for stackName := range snapshot.State.Stacks(controller.Workspace) {
		checkoutByRepo, err := controller.CheckoutsForStack(stackName)
		if err != nil {
			return err
		}
		snapshot.Checkouts[stackName] = map[string]string{}
		for repoName, checkout := range checkoutByRepo {
			snapshot.Checkouts[stackName][repoName] = checkout.Dir
		}
	}
	return nil
}

func (controller *Controller) checkoutDir(snapshot plan.Snapshot, appInstance plan.Instance) (string, error) {
	repoName := cmp.Or(snapshot.App(appInstance).Repo, controller.Workspace)
	checkoutDir := snapshot.Checkouts[appInstance.Stack][repoName]
	if checkoutDir == "" {
		return "", fmt.Errorf("%s needs repo %s, which is not cloned: clone it, or set %s", controller.processName(appInstance), repoName, snapshot.RepoPathKey(repoName))
	}
	return checkoutDir, nil
}

func (controller *Controller) saveStacks(snapshot plan.Snapshot) error {
	if err := snapshot.State.Claim(controller.Workspace, controller.Root); err != nil {
		return err
	}
	return controller.Store.Save(snapshot.State)
}

// StackDir holds the logs and the binaries of a stack.
func (controller *Controller) StackDir(stackName string) string {
	return controller.Store.StackDir(controller.Workspace, stackName)
}
