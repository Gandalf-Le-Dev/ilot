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

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/logs"
	"github.com/Gandalf-Le-Dev/ilot/internal/ports"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

type Controller struct {
	// The name of the workspace, which is also the name of its root repo.
	Workspace           string
	Config              config.Config
	Store               state.Store
	Supervisor          ProcessSupervisor
	RunBuild            func(ctx context.Context, workingDir, shellCommand string, environ []string) error
	EnsureImage         func(ctx context.Context, image string) (pullOutput string, err error)
	IsPortFreeOnHost    func(port int) bool
	IsExternalServiceUp func(port int) bool
	CheckoutsForStack   func(stackName string) (map[string]workspace.Worktree, error)
	ConfigForStack      func(stackName string) (config.Config, error)
	ReposNotCloned      map[string]string
	// What the global file holds for the workspace: values for every app, and values for one app.
	Settings  config.WorkspaceSettings
	Domain    string
	PortRange ports.Range
	ShellEnv  func() (map[string]string, error)
	Login     string
	ProxyPort string
	// The watcher of an app runs this binary, with IlotEnv: the variables that make it find the same state and the same daemon.
	IlotBinary       string
	IlotEnv          map[string]string
	ReadyTimeout     time.Duration
	ProgressInterval time.Duration
	Output           io.Writer

	configByStack map[string]config.Config
}

type StartOptions struct {
	SkipDependencies bool
}

type instance struct {
	stackName string
	appName   string
}

func (appInstance instance) processName() string {
	return appInstance.appName + "." + appInstance.stackName
}

func (controller *Controller) Start(ctx context.Context, stackName string, appNames []string, options StartOptions) (err error) {
	release, err := controller.Store.Lock()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()

	stacks, err := controller.loadStacks(stackName)
	if err != nil {
		return err
	}
	requestedInstances, err := controller.instancesOf(stackName, appNames)
	if err != nil {
		return err
	}
	startOrder, err := controller.prepareStart(stacks, requestedInstances, options)
	if err != nil {
		return err
	}

	isRequested := map[instance]bool{}
	for _, requestedInstance := range requestedInstances {
		isRequested[requestedInstance] = true
	}
	processByName, err := controller.Supervisor.Processes(ctx)
	if err != nil {
		return err
	}
	isReallyRunning := map[instance]bool{}
	hasEndedProcess := map[instance]bool{}
	for _, appInstance := range startOrder {
		process, isKnown := processByName[appInstance.processName()]
		isStoredAsRunning := stacks.isRunning(appInstance)
		isReallyRunning[appInstance] = isStoredAsRunning && isKnown && process.State != daemon.StateExited
		hasEndedProcess[appInstance] = isKnown && process.State == daemon.StateExited
	}
	if failures := controller.ensureImages(ctx, startOrder, func(appInstance instance) bool { return !isReallyRunning[appInstance] }, true); len(failures) > 0 {
		return failures[0].err
	}
	needsBuild := func(appInstance instance) bool { return isRequested[appInstance] || !isReallyRunning[appInstance] }
	if err := controller.buildApps(ctx, stacks, startOrder, needsBuild); err != nil {
		return err
	}

	for _, appInstance := range startOrder {
		needsRestart := (isRequested[appInstance] && isReallyRunning[appInstance]) || hasEndedProcess[appInstance]
		isStartedByThisRun := isRequested[appInstance] || !isReallyRunning[appInstance]
		if err := controller.startInstance(ctx, stacks, appInstance, needsRestart, isStartedByThisRun); err != nil {
			return errors.Join(err, controller.startAgainInItsStackHint(stacks, appInstance, requestedInstances, options))
		}
	}
	controller.printURLs(stacks, requestedInstances)
	return nil
}

func (controller *Controller) startAgainInItsStackHint(stacks stackSet, failedInstance instance, requestedInstances []instance, options StartOptions) error {
	if stacks.isRunning(failedInstance) {
		return nil
	}
	retryStartOrder, _ := controller.planStart(stacks, requestedInstances, options)
	if slices.Contains(retryStartOrder, failedInstance) {
		return nil
	}
	appNamesToRequest := []string{failedInstance.appName}
	for _, requestedInstance := range requestedInstances {
		appNamesToRequest = append(appNamesToRequest, requestedInstance.appName)
	}
	return fmt.Errorf("%s ran in %s: name it to start it there again: ilot start %s", failedInstance.appName, failedInstance.stackName, strings.Join(appNamesToRequest, " "))
}

func (controller *Controller) Boot(ctx context.Context, baseStackName string) (err error) {
	release, err := controller.Store.Lock()
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
	release, err := controller.Store.Lock()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()

	stacks, err := controller.loadStacks(stackName)
	if err != nil {
		return err
	}
	restartedInstances, err := controller.instancesOf(stackName, appNames)
	if err != nil {
		return err
	}
	for _, appInstance := range restartedInstances {
		if !stacks.isRunning(appInstance) {
			return fmt.Errorf("%s does not run: start it with ilot start %s", appInstance.processName(), appInstance.appName)
		}
	}
	if err := controller.buildApps(ctx, stacks, restartedInstances, func(instance) bool { return true }); err != nil {
		return err
	}
	processByName, err := controller.Supervisor.Processes(ctx)
	if err != nil {
		return err
	}
	for _, appInstance := range restartedInstances {
		// A process that the daemon does not hold starts in the apply. A restart on top would start it twice.
		_, isHeldByDaemon := processByName[appInstance.processName()]
		if err := controller.startInstance(ctx, stacks, appInstance, isHeldByDaemon, false); err != nil {
			return err
		}
	}
	controller.printURLs(stacks, restartedInstances)
	return nil
}

// The only command that replaces the daemon, for example after an upgrade of ilot.
func (controller *Controller) Restart(ctx context.Context, baseStackName string) (err error) {
	release, err := controller.Store.Lock()
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
	stacks, err := controller.loadStacks(baseStackName)
	if err != nil {
		return err
	}
	var bootInstances []instance
	for _, appName := range slices.Sorted(maps.Keys(controller.Config.Apps)) {
		if controller.Config.Apps[appName].Autostart {
			bootInstances = append(bootInstances, instance{stackName: controller.stackOf(workspace.BaseStackName, appName), appName: appName})
		}
	}
	for _, instanceRunningBeforeBoot := range stacks.runningInstances() {
		if !slices.Contains(bootInstances, instanceRunningBeforeBoot) {
			bootInstances = append(bootInstances, instanceRunningBeforeBoot)
		}
		stacks.setRunning(instanceRunningBeforeBoot, false)
	}
	startOrder, err := controller.prepareStart(stacks, bootInstances, StartOptions{SkipDependencies: false})
	if err != nil {
		return err
	}
	imageFailures := controller.ensureImages(ctx, startOrder, func(instance) bool { return true }, false)
	needsBuild := func(appInstance instance) bool { return controller.binDirIsEmpty(appInstance.stackName) }
	if err := controller.buildApps(ctx, stacks, startOrder, needsBuild); err != nil {
		return err
	}
	return controller.startEachUnlessADependencyFailed(ctx, stacks, startOrder, imageFailures)
}

// One app that fails at boot must not leave the others down: the next login finds the daemon up and boots nothing.
func (controller *Controller) startEachUnlessADependencyFailed(ctx context.Context, stacks stackSet, startOrder []instance, imageFailures []imageFailure) error {
	var problems []error
	isFailed := map[instance]bool{}
	for _, failure := range imageFailures {
		isFailed[failure.appInstance] = true
		problems = append(problems, fmt.Errorf("%s: %w", failure.appInstance.processName(), failure.err))
	}
	for _, appInstance := range startOrder {
		if isFailed[appInstance] {
			continue
		}
		if failedDependency, hasFailedDependency := controller.failedDependencyOf(stacks, appInstance, isFailed); hasFailedDependency {
			isFailed[appInstance] = true
			problems = append(problems, fmt.Errorf("%s skipped: it depends on %s", appInstance.processName(), failedDependency.processName()))
			continue
		}
		if err := controller.startInstance(ctx, stacks, appInstance, false, true); err != nil {
			isFailed[appInstance] = true
			problems = append(problems, err)
		}
	}
	if len(problems) > 0 {
		problems = append(problems, errors.New("fix the cause, then run ilot start with the apps above"))
	}
	return errors.Join(problems...)
}

func (controller *Controller) failedDependencyOf(stacks stackSet, appInstance instance, isFailed map[instance]bool) (instance, bool) {
	for _, dependency := range controller.app(appInstance).DependsOn {
		if _, isApp := controller.anyDeclaration(appInstance.stackName, dependency); !isApp {
			continue
		}
		dependencyInStackOfDependent := instance{stackName: appInstance.stackName, appName: dependency}
		if isFailed[dependencyInStackOfDependent] {
			return dependencyInStackOfDependent, true
		}
		dependencyInstance := instance{stackName: controller.stackServing(stacks, appInstance.stackName, dependency), appName: dependency}
		if isFailed[dependencyInstance] {
			return dependencyInstance, true
		}
	}
	return instance{}, false
}

func (controller *Controller) prepareStart(stacks stackSet, requestedInstances []instance, options StartOptions) ([]instance, error) {
	startOrder, externalDependencies := controller.planStart(stacks, requestedInstances, options)
	if err := controller.checkExternalServicesUp(externalDependencies); err != nil {
		return nil, err
	}
	if err := controller.allocatePorts(stacks, startOrder); err != nil {
		return nil, err
	}
	if err := controller.checkEveryProcessRenders(stacks, startOrder); err != nil {
		return nil, err
	}
	return startOrder, controller.saveStacks(stacks)
}

func (controller *Controller) instancesOf(stackName string, appNames []string) ([]instance, error) {
	var instances []instance
	for _, appName := range appNames {
		if _, isApp := controller.Config.Apps[appName]; !isApp {
			return nil, errors.Join(fmt.Errorf("unknown app %q, known apps: %v", appName, slices.Sorted(maps.Keys(controller.Config.Apps))), controller.reposNotClonedHint())
		}
		instances = append(instances, instance{stackName: controller.stackOf(stackName, appName), appName: appName})
	}
	return instances, nil
}

func (controller *Controller) reposNotClonedHint() error {
	var hints []error
	for _, repoName := range slices.Sorted(maps.Keys(controller.ReposNotCloned)) {
		hints = append(hints, fmt.Errorf("repo %s is not at %s: clone it, or set %s", repoName, controller.ReposNotCloned[repoName], controller.repoPathKey(repoName)))
	}
	return errors.Join(hints...)
}

// Where the user gives the path of a repo that is cloned at another place.
func (controller *Controller) repoPathKey(repoName string) string {
	return fmt.Sprintf("workspaces.%s.repos.%s in ~/%s", controller.Workspace, repoName, config.GlobalFilePath)
}

func (controller *Controller) stackOf(requestedStackName, appName string) string {
	if controller.Config.Apps[appName].Scope == config.ScopeBase {
		return workspace.BaseStackName
	}
	return requestedStackName
}

func (controller *Controller) startInstance(ctx context.Context, stacks stackSet, appInstance instance, needsRestart bool, isStartedByThisRun bool) error {
	stacks.setRunning(appInstance, true)
	if err := controller.saveStacks(stacks); err != nil {
		return err
	}
	if err := controller.applyAll(ctx, stacks); err != nil {
		return err
	}
	if needsRestart {
		if err := controller.Supervisor.Restart(ctx, appInstance.processName()); err != nil {
			return err
		}
	}
	fmt.Fprintf(controller.Output, "waiting for %s\n", appInstance.processName())
	stopShowingLog := logs.FollowNewMessages(controller.logFile(appInstance), &linePrefixWriter{prefix: appInstance.processName() + " | ", output: controller.Output, isAtLineStart: true})
	waitErr := controller.Supervisor.WaitReady(ctx, appInstance.processName(), controller.ReadyTimeout)
	if err := errors.Join(waitErr, stopShowingLog()); err != nil {
		failure := fmt.Errorf("%w, see %s", err, controller.logFile(appInstance))
		_, hasEnded := errors.AsType[*daemon.ExitedError](err)
		_, isNotReadyInTime := errors.AsType[*daemon.NotReadyError](err)
		switch {
		case hasEnded && isStartedByThisRun && ctx.Err() == nil:
			return errors.Join(failure, controller.stopFailedInstance(ctx, stacks, appInstance))
		case isNotReadyInTime:
			return fmt.Errorf("%w; it still starts: ilot keeps it", failure)
		}
		return failure
	}
	return nil
}

func (controller *Controller) stopFailedInstance(ctx context.Context, stacks stackSet, failedInstance instance) error {
	cleanupCtx := context.WithoutCancel(ctx)
	stacks.setRunning(failedInstance, false)
	err := controller.saveStacks(stacks)
	if err == nil {
		err = controller.applyAll(cleanupCtx, stacks)
	}
	if err != nil {
		return fmt.Errorf("stop %s after its failure: %w", failedInstance.processName(), err)
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
	release, err := controller.Store.Lock()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()

	stacks, err := controller.loadStacks(stackName)
	if err != nil {
		return err
	}
	stoppedInstances, err := controller.instancesOf(stackName, appNames)
	if err != nil {
		return err
	}
	stacksBeforeStop := stacks.clone()
	for _, stoppedInstance := range stoppedInstances {
		if !stacks.isRunning(stoppedInstance) {
			fmt.Fprintf(controller.Output, "%s is not running\n", stoppedInstance.processName())
			continue
		}
		stacks.setRunning(stoppedInstance, false)
		controller.warnAboutDependents(stacksBeforeStop, stoppedInstance)
	}
	if err := controller.saveStacks(stacks); err != nil {
		return err
	}
	return controller.applyAll(ctx, stacks)
}

func (controller *Controller) warnAboutDependents(stacksBeforeStop stackSet, stoppedInstance instance) {
	for _, runningInstance := range stacksBeforeStop.runningInstances() {
		if runningInstance == stoppedInstance || !slices.Contains(controller.app(runningInstance).DependsOn, stoppedInstance.appName) {
			continue
		}
		if controller.stackServing(stacksBeforeStop, runningInstance.stackName, stoppedInstance.appName) == stoppedInstance.stackName {
			fmt.Fprintf(controller.Output, "warning: %s depends on %s and keeps running\n", runningInstance.processName(), stoppedInstance.processName())
		}
	}
}
