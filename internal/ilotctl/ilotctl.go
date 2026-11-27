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

	"github.com/Gandalf-Le-Dev/ilot/internal/appconfig"
	"github.com/Gandalf-Le-Dev/ilot/internal/checkouts"
	"github.com/Gandalf-Le-Dev/ilot/internal/logs"
	"github.com/Gandalf-Le-Dev/ilot/internal/personal"
	"github.com/Gandalf-Le-Dev/ilot/internal/processcompose"
	"github.com/Gandalf-Le-Dev/ilot/internal/stack"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

type ProcessSupervisor interface {
	Apply(ctx context.Context, specs []processcompose.ProcessSpec) error
	Restart(ctx context.Context, processName string) error
	Statuses(ctx context.Context) (map[string]processcompose.ProcessStatus, error)
	WaitReady(ctx context.Context, processName string, timeout time.Duration) error
	IsUp(ctx context.Context) bool
	RestartCommand(processName string) string
	RecordedEnvironment() (environment map[string]string, isRecorded bool, err error)
	Shutdown() error
}

type Controller struct {
	Config              appconfig.Config
	Store               state.Store
	Supervisor          ProcessSupervisor
	RunBuild            func(ctx context.Context, workingDir, shellCommand string, environ []string) error
	EnsureImage         func(ctx context.Context, image string) (pullOutput string, err error)
	IsPortFreeOnHost    func(port int) bool
	IsExternalServiceUp func(port int) bool
	CheckoutsForStack   func(stackName string) (map[string]checkouts.Checkout, error)
	ConfigForStack      func(stackName string) (appconfig.Config, error)
	UsesMise            func(checkoutDir string) bool
	ReposNotCloned      map[string]string
	Personal            personal.Settings
	KeyringFile         string
	ShellEnv            func() (map[string]string, error)
	Login               string
	RoutesDir           string
	BackendHost         string
	ProxyPort           string
	ReadyTimeout        time.Duration
	ProgressInterval    time.Duration
	Output              io.Writer

	configByStack      map[string]appconfig.Config
	resolvedShellEnv   map[string]string
	isShellEnvResolved bool
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

func (controller *Controller) Start(ctx context.Context, worktree stack.Worktree, appNames []string, options StartOptions) (err error) {
	release, err := controller.Store.Lock()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()

	stacks, err := controller.loadStacks(worktree)
	if err != nil {
		return err
	}
	controller.warnWhenKeyringKeyIsMissing()
	requestedInstances, err := controller.instancesOf(worktree.Name, appNames)
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
	statusByProcess, err := controller.Supervisor.Statuses(ctx)
	if err != nil {
		return err
	}
	isReallyRunning := map[instance]bool{}
	hasEndedProcess := map[instance]bool{}
	for _, appInstance := range startOrder {
		status, isKnown := statusByProcess[appInstance.processName()]
		isStoredAsRunning := stacks.isRunning(appInstance)
		isReallyRunning[appInstance] = isStoredAsRunning && isKnown && status.IsRunningOrStarting()
		hasEndedProcess[appInstance] = isKnown && !status.IsRunningOrStarting()
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

func (controller *Controller) Boot(ctx context.Context, mainCheckout stack.Worktree) (err error) {
	release, err := controller.Store.Lock()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()

	if controller.Supervisor.IsUp(ctx) {
		fmt.Fprintln(controller.Output, "ilot already runs, nothing to boot")
		return nil
	}
	return controller.bootApps(ctx, mainCheckout)
}

// The daemon gives every app the environment it started with: only a new daemon reads the shell files again.
func (controller *Controller) Restart(ctx context.Context, mainCheckout stack.Worktree) (err error) {
	release, err := controller.Store.Lock()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()

	if controller.Supervisor.IsUp(ctx) {
		fmt.Fprintln(controller.Output, "stopping every app of every stack")
	}
	if err := controller.Supervisor.Shutdown(); err != nil {
		return err
	}
	return controller.bootApps(ctx, mainCheckout)
}

func (controller *Controller) bootApps(ctx context.Context, mainCheckout stack.Worktree) error {
	stacks, err := controller.loadStacks(mainCheckout)
	if err != nil {
		return err
	}
	var bootInstances []instance
	for _, appName := range slices.Sorted(maps.Keys(controller.Config.Apps)) {
		if controller.Config.Apps[appName].Autostart {
			bootInstances = append(bootInstances, instance{stackName: controller.stackOf(stack.MasterStackName, appName), appName: appName})
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
		hints = append(hints, fmt.Errorf("repo %s is not at %s: clone it, or set repos.%s.path in ~/.config/ilot/env.yaml", repoName, controller.ReposNotCloned[repoName], repoName))
	}
	return errors.Join(hints...)
}

func (controller *Controller) stackOf(requestedStackName, appName string) string {
	if controller.Config.Apps[appName].Shared {
		return stack.SharedStackName
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
		var endedError *processcompose.ProcessEndedError
		var notReadyInTimeError *processcompose.ProcessNotReadyInTimeError
		switch {
		case errors.As(err, &endedError) && isStartedByThisRun && ctx.Err() == nil:
			return errors.Join(failure, controller.stopFailedInstance(ctx, stacks, appInstance))
		case errors.As(err, &notReadyInTimeError):
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

func (controller *Controller) Stop(ctx context.Context, worktree stack.Worktree, appNames []string) (err error) {
	release, err := controller.Store.Lock()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()

	stacks, err := controller.loadStacks(worktree)
	if err != nil {
		return err
	}
	stoppedInstances, err := controller.instancesOf(worktree.Name, appNames)
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
