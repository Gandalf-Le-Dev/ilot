package ilotctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/logs"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
	"github.com/Gandalf-Le-Dev/ilot/internal/ports"
)

// Controller runs the commands that change the stacks: it plans with the snapshot of its Source, then builds, calls the daemon and waits.
type Controller struct {
	Source
	Supervisor          ProcessSupervisor
	RunBuild            func(ctx context.Context, workingDir, shellCommand string, environ []string) error
	EnsureImage         func(ctx context.Context, image string) (pullOutput string, err error)
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

func (controller *Controller) Start(ctx context.Context, stackName string, appNames []string, options StartOptions) (err error) {
	release, err := controller.Store.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()

	snapshot, err := controller.loadStacks(stackName)
	if err != nil {
		return err
	}
	requestedInstances, err := snapshot.Instances(stackName, appNames)
	if err != nil {
		return err
	}
	startOrder, err := controller.prepareStart(snapshot, requestedInstances, options)
	if err != nil {
		return err
	}

	isRequested := map[plan.Instance]bool{}
	for _, requestedInstance := range requestedInstances {
		isRequested[requestedInstance] = true
	}
	processByName, err := controller.Supervisor.Processes(ctx)
	if err != nil {
		return err
	}
	isReallyRunning := map[plan.Instance]bool{}
	hasEndedProcess := map[plan.Instance]bool{}
	for _, appInstance := range startOrder {
		process, isKnown := processByName[plan.ProcessName(controller.Workspace, appInstance)]
		isStoredAsRunning := snapshot.IsWanted(appInstance)
		isReallyRunning[appInstance] = isStoredAsRunning && isKnown && process.State != daemon.StateExited
		hasEndedProcess[appInstance] = isKnown && process.State == daemon.StateExited
	}
	if failures := controller.ensureImages(snapshot, ctx, startOrder, func(appInstance plan.Instance) bool { return !isReallyRunning[appInstance] }, true); len(failures) > 0 {
		return failures[0].err
	}
	needsBuild := func(appInstance plan.Instance) bool { return isRequested[appInstance] || !isReallyRunning[appInstance] }
	if err := controller.buildApps(ctx, snapshot, startOrder, needsBuild); err != nil {
		return err
	}

	for _, appInstance := range startOrder {
		needsRestart := (isRequested[appInstance] && isReallyRunning[appInstance]) || hasEndedProcess[appInstance]
		isStartedByThisRun := isRequested[appInstance] || !isReallyRunning[appInstance]
		if err := controller.startInstance(ctx, snapshot, appInstance, needsRestart, isStartedByThisRun); err != nil {
			return errors.Join(err, controller.startAgainInItsStackHint(snapshot, appInstance, requestedInstances, options))
		}
	}
	controller.printURLs(snapshot, requestedInstances)
	return nil
}

func (controller *Controller) startAgainInItsStackHint(snapshot plan.Snapshot, failedInstance plan.Instance, requestedInstances []plan.Instance, options StartOptions) error {
	if snapshot.IsWanted(failedInstance) {
		return nil
	}
	retryStartOrder, _ := snapshot.StartOrder(requestedInstances, options.SkipDependencies)
	if slices.Contains(retryStartOrder, failedInstance) {
		return nil
	}
	appNamesToRequest := []string{failedInstance.App}
	for _, requestedInstance := range requestedInstances {
		appNamesToRequest = append(appNamesToRequest, requestedInstance.App)
	}
	return fmt.Errorf("%s ran in %s: name it to start it there again: ilot start %s", failedInstance.App, failedInstance.Stack, strings.Join(appNamesToRequest, " "))
}

func (controller *Controller) Boot(ctx context.Context, baseStackName string) (err error) {
	release, err := controller.Store.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()

	if controller.Supervisor.IsUp(ctx) {
		fmt.Fprintln(controller.Output, "ilot already runs, nothing to boot")
		return nil
	}
	return controller.bootApps(ctx, baseStackName)
}

// RestartApps builds the named apps again, then restarts them. The watcher of an app runs it.
// A build that fails keeps the running app.
func (controller *Controller) RestartApps(ctx context.Context, stackName string, appNames []string) (err error) {
	release, err := controller.Store.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()

	snapshot, err := controller.loadStacks(stackName)
	if err != nil {
		return err
	}
	restartedInstances, err := snapshot.Instances(stackName, appNames)
	if err != nil {
		return err
	}
	for _, appInstance := range restartedInstances {
		if !snapshot.IsWanted(appInstance) {
			return fmt.Errorf("%s does not run: start it with ilot start %s", plan.ProcessName(controller.Workspace, appInstance), appInstance.App)
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
		// A process that the daemon does not hold starts in the apply. A restart on top would start it twice.
		_, isHeldByDaemon := processByName[plan.ProcessName(controller.Workspace, appInstance)]
		if err := controller.startInstance(ctx, snapshot, appInstance, isHeldByDaemon, false); err != nil {
			return err
		}
	}
	controller.printURLs(snapshot, restartedInstances)
	return nil
}

// The only command that replaces the daemon, for example after an upgrade of ilot.
func (controller *Controller) Restart(ctx context.Context, baseStackName string) (err error) {
	release, err := controller.Store.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()

	if controller.Supervisor.IsUp(ctx) {
		fmt.Fprintln(controller.Output, "stopping every app of every stack")
	}
	if err := controller.Supervisor.Shutdown(ctx); err != nil {
		return err
	}
	return controller.bootApps(ctx, baseStackName)
}

func (controller *Controller) bootApps(ctx context.Context, baseStackName string) error {
	snapshot, err := controller.loadStacks(baseStackName)
	if err != nil {
		return err
	}
	var bootInstances []plan.Instance
	for _, appName := range slices.Sorted(maps.Keys(controller.Config.Apps)) {
		if controller.Config.Apps[appName].Autostart {
			bootInstances = append(bootInstances, plan.Instance{Stack: snapshot.StackOf(plan.BaseStack, appName), App: appName})
		}
	}
	for _, instanceRunningBeforeBoot := range snapshot.Wanted() {
		if !slices.Contains(bootInstances, instanceRunningBeforeBoot) {
			bootInstances = append(bootInstances, instanceRunningBeforeBoot)
		}
		snapshot.SetWanted(instanceRunningBeforeBoot, false)
	}
	startOrder, err := controller.prepareStart(snapshot, bootInstances, StartOptions{SkipDependencies: false})
	if err != nil {
		return err
	}
	imageFailures := controller.ensureImages(snapshot, ctx, startOrder, func(plan.Instance) bool { return true }, false)
	needsBuild := func(appInstance plan.Instance) bool { return controller.binDirIsEmpty(appInstance.Stack) }
	if err := controller.buildApps(ctx, snapshot, startOrder, needsBuild); err != nil {
		return err
	}
	return controller.startEachUnlessADependencyFailed(ctx, snapshot, startOrder, imageFailures)
}

// One app that fails at boot must not leave the others down: the next login finds the daemon up and boots nothing.
func (controller *Controller) startEachUnlessADependencyFailed(ctx context.Context, snapshot plan.Snapshot, startOrder []plan.Instance, imageFailures []imageFailure) error {
	var problems []error
	isFailed := map[plan.Instance]bool{}
	for _, failure := range imageFailures {
		isFailed[failure.appInstance] = true
		problems = append(problems, fmt.Errorf("%s: %w", plan.ProcessName(controller.Workspace, failure.appInstance), failure.err))
	}
	for _, appInstance := range startOrder {
		if isFailed[appInstance] {
			continue
		}
		if failedDependency, hasFailedDependency := controller.failedDependencyOf(snapshot, appInstance, isFailed); hasFailedDependency {
			isFailed[appInstance] = true
			problems = append(problems, fmt.Errorf("%s skipped: it depends on %s", plan.ProcessName(controller.Workspace, appInstance), plan.ProcessName(controller.Workspace, failedDependency)))
			continue
		}
		if err := controller.startInstance(ctx, snapshot, appInstance, false, true); err != nil {
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

func (controller *Controller) prepareStart(snapshot plan.Snapshot, requestedInstances []plan.Instance, options StartOptions) ([]plan.Instance, error) {
	start := snapshot.PlanStart(requestedInstances, options.SkipDependencies)
	if err := controller.checkExternalServicesUp(start.Externals); err != nil {
		return nil, err
	}
	if err := controller.allocatePorts(snapshot, start); err != nil {
		return nil, err
	}
	if err := controller.checkEveryProcessRenders(snapshot, start.Order); err != nil {
		return nil, err
	}
	return start.Order, controller.saveStacks(snapshot)
}

func (controller *Controller) startInstance(ctx context.Context, snapshot plan.Snapshot, appInstance plan.Instance, needsRestart bool, isStartedByThisRun bool) error {
	snapshot.SetWanted(appInstance, true)
	if err := controller.saveStacks(snapshot); err != nil {
		return err
	}
	if err := controller.applyAll(ctx, snapshot); err != nil {
		return err
	}
	if needsRestart {
		if err := controller.Supervisor.Restart(ctx, plan.ProcessName(controller.Workspace, appInstance)); err != nil {
			return err
		}
	}
	fmt.Fprintf(controller.Output, "waiting for %s\n", plan.ProcessName(controller.Workspace, appInstance))
	stopShowingLog := logs.FollowNewMessages(snapshot.LogFile(appInstance), &linePrefixWriter{prefix: plan.ProcessName(controller.Workspace, appInstance) + " | ", output: controller.Output, isAtLineStart: true})
	waitErr := controller.Supervisor.WaitReady(ctx, plan.ProcessName(controller.Workspace, appInstance), controller.ReadyTimeout)
	if err := errors.Join(waitErr, stopShowingLog()); err != nil {
		failure := fmt.Errorf("%w, see %s", err, snapshot.LogFile(appInstance))
		_, hasEnded := errors.AsType[*daemon.ExitedError](err)
		_, isNotReadyInTime := errors.AsType[*daemon.NotReadyError](err)
		switch {
		case hasEnded && isStartedByThisRun && ctx.Err() == nil:
			return errors.Join(failure, controller.stopFailedInstance(ctx, snapshot, appInstance))
		case isNotReadyInTime:
			return fmt.Errorf("%w; it still starts: ilot keeps it", failure)
		}
		return failure
	}
	return nil
}

func (controller *Controller) stopFailedInstance(ctx context.Context, snapshot plan.Snapshot, failedInstance plan.Instance) error {
	cleanupCtx := context.WithoutCancel(ctx)
	snapshot.SetWanted(failedInstance, false)
	err := controller.saveStacks(snapshot)
	if err == nil {
		err = controller.applyAll(cleanupCtx, snapshot)
	}
	if err != nil {
		return fmt.Errorf("stop %s after its failure: %w", plan.ProcessName(controller.Workspace, failedInstance), err)
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

func (controller *Controller) Stop(ctx context.Context, stackName string, appNames []string) (err error) {
	release, err := controller.Store.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()

	snapshot, err := controller.loadStacks(stackName)
	if err != nil {
		return err
	}
	stoppedInstances, err := snapshot.Instances(stackName, appNames)
	if err != nil {
		return err
	}
	stacksBeforeStop := snapshot.Clone()
	for _, stoppedInstance := range stoppedInstances {
		if !snapshot.IsWanted(stoppedInstance) {
			fmt.Fprintf(controller.Output, "%s is not running\n", plan.ProcessName(controller.Workspace, stoppedInstance))
			continue
		}
		snapshot.SetWanted(stoppedInstance, false)
		controller.warnAboutDependents(stacksBeforeStop, stoppedInstance)
	}
	if err := controller.saveStacks(snapshot); err != nil {
		return err
	}
	return controller.applyAll(ctx, snapshot)
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
