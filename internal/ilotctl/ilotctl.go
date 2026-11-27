package ilotctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/logs"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
	"github.com/Gandalf-Le-Dev/ilot/internal/ports"
)

// Controller runs the commands that change the stacks: it plans with the snapshot of its Source, then builds, calls the daemon and waits.
type Controller struct {
	Source
	Supervisor  ProcessSupervisor
	RunBuild    func(ctx context.Context, workingDir, shellCommand string, environ []string) error
	EnsureImage func(ctx context.Context, image string) (pullOutput string, err error)
	// RemoveContainer removes a container by name, and is no error for a container that does not exist.
	RemoveContainer     func(ctx context.Context, name string) error
	IsPortFreeOnHost    func(port int) bool
	IsExternalServiceUp func(port int) bool
	PortRange           ports.Range
	ReadyTimeout        time.Duration
	ProgressInterval    time.Duration
	Output              io.Writer
}

type StartOptions struct {
	SkipDependencies bool
}

func (controller *Controller) Start(ctx context.Context, stackName string, appNames []string, options StartOptions) error {
	var before plan.Snapshot
	var dropped, requestedInstances []plan.Instance
	var start plan.Start
	err := controller.underLock(ctx, func() (err error) {
		if before, dropped, err = controller.loadStacks(stackName); err != nil {
			return err
		}
		if requestedInstances, err = before.Instances(stackName, appNames); err != nil {
			return err
		}
		start, err = controller.prepareStart(before, requestedInstances, options)
		return err
	})
	if err != nil {
		return err
	}
	if err := controller.deleteProcesses(ctx, before, dropped); err != nil {
		return err
	}
	after := start.After
	processByName, err := controller.Supervisor.Processes(ctx)
	if err != nil {
		return err
	}
	isRequested := map[plan.Instance]bool{}
	for _, requestedInstance := range requestedInstances {
		isRequested[requestedInstance] = true
	}
	isRunning := func(appInstance plan.Instance) bool {
		process, isHeld := processByName[after.ProcessName(appInstance)]
		return isHeld && process.State != daemon.StateExited
	}
	if failures := controller.ensureImages(after, ctx, start.Order, func(appInstance plan.Instance) bool { return !isRunning(appInstance) }, true); len(failures) > 0 {
		return failures[0].err
	}
	needsBuild := func(appInstance plan.Instance) bool { return isRequested[appInstance] || !isRunning(appInstance) }
	if err := controller.buildApps(ctx, after, start.Order, needsBuild); err != nil {
		return err
	}
	for _, appInstance := range start.Order {
		if err := controller.startInstance(ctx, after, appInstance, processByName, isRequested[appInstance]); err != nil {
			return err
		}
	}
	if err := controller.replaceChangedDependents(ctx, before, after, stackName, start.Order, processByName); err != nil {
		return err
	}
	if err := controller.sendRoutes(ctx); err != nil {
		return err
	}
	controller.printURLs(after, requestedInstances)
	return nil
}

// underLock runs a step that reads or changes the state. It holds the state lock for that step only: never for a build, a wait or a daemon call.
func (controller *Controller) underLock(ctx context.Context, step func() error) (err error) {
	release, err := controller.Store.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	return step()
}

// sendRoutes reads the state again, because another command can have changed it since this one planned.
func (controller *Controller) sendRoutes(ctx context.Context) error {
	current, err := controller.loadState()
	if err != nil {
		return err
	}
	return controller.Supervisor.SetRoutes(ctx, plan.Snapshot{Workspace: controller.Workspace, State: current, Machine: controller.Machine}.Routes())
}

// replaceChangedDependents replaces each running app of the stack whose specification this start changed, for example a template that now names an app of the stack.
// Both specifications come from the same shell environment, so a change of the shell or of the terminal replaces nothing.
func (controller *Controller) replaceChangedDependents(ctx context.Context, before, after plan.Snapshot, stackName string, startOrder []plan.Instance, processByName map[string]daemon.Process) error {
	for _, wantedInstance := range after.Wanted() {
		process, isHeld := processByName[after.ProcessName(wantedInstance)]
		if wantedInstance.Stack != stackName || slices.Contains(startOrder, wantedInstance) || !isHeld || process.State == daemon.StateExited {
			continue
		}
		specAfter, err := after.Spec(wantedInstance)
		if err != nil {
			return err
		}
		specBefore, err := before.Spec(wantedInstance)
		if err == nil && specBefore.Hash() == specAfter.Hash() {
			continue
		}
		fmt.Fprintf(controller.Output, "restarting %s: this start changed its specification\n", specAfter.Name)
		if err := controller.Supervisor.Put(ctx, specAfter); err != nil {
			return err
		}
	}
	return nil
}

func (controller *Controller) Boot(ctx context.Context, baseStackName string) error {
	if controller.Supervisor.IsUp(ctx) {
		fmt.Fprintln(controller.Output, "ilot already runs, nothing to boot")
		return nil
	}
	return controller.bootApps(ctx, baseStackName)
}

// RestartApps builds the named apps again, then restarts them. The watcher of an app runs it.
// A build that fails keeps the running app.
func (controller *Controller) RestartApps(ctx context.Context, stackName string, appNames []string) error {
	var snapshot plan.Snapshot
	var dropped []plan.Instance
	err := controller.underLock(ctx, func() (err error) {
		snapshot, dropped, err = controller.loadStacks(stackName)
		return err
	})
	if err != nil {
		return err
	}
	if err := controller.deleteProcesses(ctx, snapshot, dropped); err != nil {
		return err
	}
	restartedInstances, err := snapshot.Instances(stackName, appNames)
	if err != nil {
		return err
	}
	for _, appInstance := range restartedInstances {
		if !snapshot.IsWanted(appInstance) {
			return fmt.Errorf("%s does not run: start it with ilot start %s", snapshot.ProcessName(appInstance), appInstance.App)
		}
	}
	if err := controller.buildApps(ctx, snapshot, restartedInstances, func(plan.Instance) bool { return true }); err != nil {
		return err
	}
	processByName, err := controller.Supervisor.Processes(ctx)
	if err != nil {
		return err
	}
	for _, appInstance := range restartedInstances {
		if err := controller.startInstance(ctx, snapshot, appInstance, processByName, true); err != nil {
			return err
		}
	}
	controller.printURLs(snapshot, restartedInstances)
	return nil
}

// The only command that replaces the daemon, for example after an upgrade of ilot.
func (controller *Controller) Restart(ctx context.Context, baseStackName string) error {
	if controller.Supervisor.IsUp(ctx) {
		fmt.Fprintln(controller.Output, "stopping every app of every stack")
	}
	if err := controller.Supervisor.Shutdown(ctx); err != nil {
		return err
	}
	return controller.bootApps(ctx, baseStackName)
}

func (controller *Controller) bootApps(ctx context.Context, baseStackName string) error {
	var start plan.Start
	var problems []error
	err := controller.underLock(ctx, func() error {
		snapshot, _, err := controller.loadStacks(baseStackName)
		if err != nil {
			return err
		}
		start, problems, err = controller.planBoot(snapshot)
		return err
	})
	if err != nil {
		return err
	}
	imageFailures := controller.ensureImages(start.After, ctx, start.Order, func(plan.Instance) bool { return true }, false)
	needsBuild := func(appInstance plan.Instance) bool { return controller.binDirIsEmpty(appInstance.Stack) }
	if err := controller.buildApps(ctx, start.After, start.Order, needsBuild); err != nil {
		return err
	}
	err = controller.startEachUnlessADependencyFailed(ctx, start.After, start.Order, imageFailures, map[string]daemon.Process{})
	return errors.Join(append(problems, err, controller.sendRoutes(ctx))...)
}

// StartAll starts each member of each stack of the workspace that does not run, after its dependencies: what a crash of the daemon stopped comes back.
// A member that runs gets no call.
func (controller *Controller) StartAll(ctx context.Context) error {
	var start plan.Start
	var problems []error
	err := controller.underLock(ctx, func() error {
		snapshot, _, err := controller.loadStacks(plan.BaseStack)
		if err != nil {
			return err
		}
		start, problems, err = controller.planMembers(snapshot, nil)
		return err
	})
	if err != nil {
		return err
	}
	processByName, err := controller.Supervisor.Processes(ctx)
	if err != nil {
		return err
	}
	isDown := func(appInstance plan.Instance) bool {
		process, isHeld := processByName[start.After.ProcessName(appInstance)]
		return !isHeld || process.State == daemon.StateExited
	}
	imageFailures := controller.ensureImages(start.After, ctx, start.Order, isDown, false)
	if err := controller.buildApps(ctx, start.After, start.Order, isDown); err != nil {
		return err
	}
	err = controller.startEachUnlessADependencyFailed(ctx, start.After, start.Order, imageFailures, processByName)
	return errors.Join(append(problems, err, controller.sendRoutes(ctx))...)
}

func (controller *Controller) planBoot(snapshot plan.Snapshot) (plan.Start, []error, error) {
	var bootInstances []plan.Instance
	for _, appName := range slices.Sorted(maps.Keys(controller.Config.Apps)) {
		if controller.Config.Apps[appName].Autostart {
			bootInstances = append(bootInstances, plan.Instance{Stack: snapshot.StackOf(plan.BaseStack, appName), App: appName})
		}
	}
	return controller.planMembers(snapshot, bootInstances)
}

// planMembers plans the start of every member of every stack, after the given instances.
// It reads the file of each stack. A stack whose file is broken stays down, and the others start.
func (controller *Controller) planMembers(snapshot plan.Snapshot, bootInstances []plan.Instance) (plan.Start, []error, error) {
	var problems []error
	for _, wantedInstance := range snapshot.Wanted() {
		if err := controller.addStack(snapshot, wantedInstance.Stack); err != nil {
			problems = append(problems, err)
			continue
		}
		if !slices.Contains(bootInstances, wantedInstance) {
			bootInstances = append(bootInstances, wantedInstance)
		}
	}
	start, err := controller.prepareStart(snapshot, bootInstances, StartOptions{SkipDependencies: false})
	return start, problems, err
}

// One app that fails at boot must not leave the others down: the next login finds the daemon up and boots nothing.
func (controller *Controller) startEachUnlessADependencyFailed(ctx context.Context, snapshot plan.Snapshot, startOrder []plan.Instance, imageFailures []imageFailure, processByName map[string]daemon.Process) error {
	var problems []error
	isFailed := map[plan.Instance]bool{}
	for _, failure := range imageFailures {
		isFailed[failure.appInstance] = true
		problems = append(problems, fmt.Errorf("%s: %w", snapshot.ProcessName(failure.appInstance), failure.err))
	}
	for _, appInstance := range startOrder {
		if isFailed[appInstance] {
			continue
		}
		if failedDependency, hasFailedDependency := controller.failedDependencyOf(snapshot, appInstance, isFailed); hasFailedDependency {
			isFailed[appInstance] = true
			problems = append(problems, fmt.Errorf("%s skipped: it depends on %s", snapshot.ProcessName(appInstance), snapshot.ProcessName(failedDependency)))
			continue
		}
		if err := controller.startInstance(ctx, snapshot, appInstance, processByName, false); err != nil {
			isFailed[appInstance] = true
			problems = append(problems, err)
		}
	}
	if len(problems) > 0 {
		problems = append(problems, errors.New("fix the cause, then run ilot start with the apps above"))
	}
	return errors.Join(problems...)
}

func (controller *Controller) failedDependencyOf(snapshot plan.Snapshot, appInstance plan.Instance, isFailed map[plan.Instance]bool) (plan.Instance, bool) {
	for _, dependency := range snapshot.App(appInstance).DependsOn {
		if _, isApp := snapshot.AnyDeclaration(appInstance.Stack, dependency); !isApp {
			continue
		}
		dependencyInStackOfDependent := plan.Instance{Stack: appInstance.Stack, App: dependency}
		if isFailed[dependencyInStackOfDependent] {
			return dependencyInStackOfDependent, true
		}
		dependencyInstance := plan.Instance{Stack: snapshot.Serving(appInstance.Stack, dependency), App: dependency}
		if isFailed[dependencyInstance] {
			return dependencyInstance, true
		}
	}
	return plan.Instance{}, false
}

// prepareStart plans a start, gives its ports, and saves the state after it: every instance of the plan is a member from now on.
// A failed start keeps them members, so the next ilot start tries them again in the same stacks.
func (controller *Controller) prepareStart(before plan.Snapshot, requestedInstances []plan.Instance, options StartOptions) (plan.Start, error) {
	start := before.PlanStart(requestedInstances, options.SkipDependencies)
	if err := controller.checkExternalServicesUp(start.Externals); err != nil {
		return plan.Start{}, err
	}
	if err := controller.allocatePorts(before, start); err != nil {
		return plan.Start{}, err
	}
	for _, appInstance := range start.Order {
		if _, err := start.After.Spec(appInstance); err != nil {
			return plan.Start{}, err
		}
	}
	start.After.RecordRoutes(start.Order)
	return start, controller.saveStacks(start.After)
}

// startInstance sends one instance to the daemon and waits until it is ready.
// A requested instance starts again: with Put when its specification changed, which replaces it one time, and with Restart when it did not.
// A dependency that runs gets no call, so a change that a command did not ask for never replaces it.
func (controller *Controller) startInstance(ctx context.Context, snapshot plan.Snapshot, appInstance plan.Instance, processByName map[string]daemon.Process, isRequested bool) error {
	spec, err := snapshot.Spec(appInstance)
	if err != nil {
		return err
	}
	process, isHeld := processByName[spec.Name]
	isRunning := isHeld && process.State != daemon.StateExited
	switch {
	case !isRunning:
		if err := controller.put(ctx, spec); err != nil {
			return err
		}
	case isRequested && process.Hash != spec.Hash():
		fmt.Fprintf(controller.Output, "restarting %s with its new specification\n", spec.Name)
		if err := controller.put(ctx, spec); err != nil {
			return err
		}
	case isRequested:
		fmt.Fprintf(controller.Output, "restarting %s\n", spec.Name)
		if err := controller.Supervisor.Restart(ctx, spec.Name); err != nil {
			return err
		}
	}
	if !isRunning || isRequested {
		if err := controller.putWatcher(ctx, snapshot, appInstance, processByName); err != nil {
			return err
		}
	}
	return controller.waitReady(ctx, snapshot, appInstance)
}

func (controller *Controller) put(ctx context.Context, spec plan.ProcessSpec) error {
	if err := os.MkdirAll(filepath.Dir(spec.LogFile), 0o700); err != nil {
		return err
	}
	return controller.Supervisor.Put(ctx, spec)
}

// The watcher of an app restarts only when its own specification changed: it runs ilot restart, which rebuilds the app itself.
func (controller *Controller) putWatcher(ctx context.Context, snapshot plan.Snapshot, appInstance plan.Instance, processByName map[string]daemon.Process) error {
	watcher, isWatched, err := snapshot.Watcher(appInstance)
	if err != nil || !isWatched {
		return err
	}
	if process, isHeld := processByName[watcher.Name]; isHeld && process.State != daemon.StateExited && process.Hash == watcher.Hash() {
		return nil
	}
	return controller.put(ctx, watcher)
}

func (controller *Controller) waitReady(ctx context.Context, snapshot plan.Snapshot, appInstance plan.Instance) error {
	processName := snapshot.ProcessName(appInstance)
	fmt.Fprintf(controller.Output, "waiting for %s\n", processName)
	stopShowingLog := logs.FollowNewMessages(snapshot.LogFile(appInstance), &linePrefixWriter{prefix: processName + " | ", output: controller.Output, isAtLineStart: true})
	waitErr := controller.Supervisor.WaitReady(ctx, processName, controller.ReadyTimeout)
	if err := errors.Join(waitErr, stopShowingLog()); err != nil {
		failure := fmt.Errorf("%w, see %s", err, snapshot.LogFile(appInstance))
		if _, isNotReadyInTime := errors.AsType[*daemon.NotReadyError](err); isNotReadyInTime {
			return fmt.Errorf("%w; it still starts: ilot keeps it", failure)
		}
		return failure
	}
	return nil
}

// Silent builds (go build) otherwise look like ilot hangs.
func (controller *Controller) printProgressUntilStopped(task string) (stop func()) {
	if controller.ProgressInterval <= 0 {
		return func() {}
	}
	startedAt := time.Now()
	stopRequested := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(controller.ProgressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopRequested:
				return
			case <-ticker.C:
				fmt.Fprintf(controller.Output, "still %s, %s\n", task, time.Since(startedAt).Round(time.Second))
			}
		}
	}()
	return func() {
		close(stopRequested)
		<-stopped
	}
}

type linePrefixWriter struct {
	prefix        string
	output        io.Writer
	isAtLineStart bool
}

func (writer *linePrefixWriter) Write(content []byte) (int, error) {
	for _, line := range bytes.SplitAfter(content, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		if writer.isAtLineStart {
			if _, err := io.WriteString(writer.output, writer.prefix); err != nil {
				return 0, err
			}
		}
		if _, err := writer.output.Write(line); err != nil {
			return 0, err
		}
		writer.isAtLineStart = line[len(line)-1] == '\n'
	}
	return len(content), nil
}

func (controller *Controller) Stop(ctx context.Context, stackName string, appNames []string) error {
	var snapshot plan.Snapshot
	var stopped []plan.Instance
	err := controller.underLock(ctx, func() (err error) {
		snapshot, stopped, err = controller.planStop(stackName, appNames)
		return err
	})
	if err != nil {
		return err
	}
	if err := controller.deleteProcesses(ctx, snapshot, stopped); err != nil {
		return err
	}
	return controller.sendRoutes(ctx)
}

func (controller *Controller) planStop(stackName string, appNames []string) (plan.Snapshot, []plan.Instance, error) {
	current, err := controller.loadState()
	if err != nil {
		return plan.Snapshot{}, nil, err
	}
	stoppedInstances, err := controller.instancesToStop(current, stackName, appNames)
	if err != nil {
		return plan.Snapshot{}, nil, err
	}
	// The warning about dependents needs the configuration. Without it, the stop still works.
	before, configErr := controller.snapshotOf(current.Clone(), stackName)
	snapshot := plan.Snapshot{Workspace: controller.Workspace, Own: stackName, State: current, Machine: controller.Machine}
	var stopped []plan.Instance
	for _, stoppedInstance := range stoppedInstances {
		if !snapshot.IsWanted(stoppedInstance) {
			fmt.Fprintf(controller.Output, "%s is not running\n", snapshot.ProcessName(stoppedInstance))
			continue
		}
		snapshot.SetWanted(stoppedInstance, false)
		stopped = append(stopped, stoppedInstance)
		if configErr == nil {
			controller.warnAboutDependents(before, stoppedInstance)
		}
	}
	return snapshot, stopped, controller.saveStacks(snapshot)
}

// deleteProcesses stops instances and their watchers. A container that the daemon does not hold, after a crash of the daemon, is removed too.
func (controller *Controller) deleteProcesses(ctx context.Context, snapshot plan.Snapshot, instances []plan.Instance) error {
	if len(instances) == 0 {
		return nil
	}
	processByName, err := controller.Supervisor.Processes(ctx)
	if err != nil {
		return err
	}
	for _, deletedInstance := range instances {
		processName := snapshot.ProcessName(deletedInstance)
		watcherName := snapshot.ProcessName(plan.Instance{Stack: deletedInstance.Stack, App: deletedInstance.App + plan.WatcherSuffix})
		for _, name := range []string{watcherName, processName} {
			if err := controller.Supervisor.Delete(ctx, name); err != nil {
				return err
			}
		}
		containerName := snapshot.State.App(controller.Workspace, deletedInstance.Stack, deletedInstance.App).Container
		if _, isHeld := processByName[processName]; containerName != "" && !isHeld && controller.RemoveContainer != nil {
			if err := controller.RemoveContainer(ctx, containerName); err != nil {
				return fmt.Errorf("remove the container %s of %s: %w", containerName, processName, err)
			}
		}
	}
	return nil
}

func (controller *Controller) warnAboutDependents(snapshotBeforeStop plan.Snapshot, stoppedInstance plan.Instance) {
	for _, runningInstance := range snapshotBeforeStop.Wanted() {
		if runningInstance == stoppedInstance || !slices.Contains(snapshotBeforeStop.App(runningInstance).DependsOn, stoppedInstance.App) {
			continue
		}
		if snapshotBeforeStop.Serving(runningInstance.Stack, stoppedInstance.App) == stoppedInstance.Stack {
			fmt.Fprintf(controller.Output, "warning: %s depends on %s and keeps running\n", plan.ProcessName(controller.Workspace, runningInstance), plan.ProcessName(controller.Workspace, stoppedInstance))
		}
	}
}
