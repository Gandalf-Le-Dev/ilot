// Package engine runs the commands that change the stacks: it takes the state lock to load, plan and save, then builds, calls the daemon for each process and waits.
package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/logs"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
	"github.com/Gandalf-Le-Dev/ilot/internal/ports"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

// Executor runs the commands that change the stacks: it plans with the snapshot of its Source, then builds, calls the daemon and waits.
type Executor struct {
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
	// Output takes the progress lines and the warnings, and Data the results, such as the URL of each started app. Without Data, the results go to Output.
	Output io.Writer
	Data   io.Writer
	// The audit log, with no record when it is nil.
	Logger *slog.Logger
}

type StartOptions struct {
	SkipDependencies bool
}

func (executor *Executor) Start(ctx context.Context, stackName string, appNames []string, options StartOptions) error {
	// A start tests the ports of its apps, so it needs a daemon that has killed what a dead daemon left behind. That can take seconds, so not under the lock.
	if err := executor.Supervisor.Up(ctx); err != nil {
		return err
	}
	machine, err := executor.machineWithShell()
	if err != nil {
		return err
	}
	var processByName map[string]daemon.Process
	var before plan.Snapshot
	var dropped, requestedInstances []plan.Instance
	var start plan.Start
	err = executor.underLock(ctx, func() (err error) {
		if processByName, err = executor.Supervisor.Processes(ctx); err != nil {
			return err
		}
		if before, dropped, err = executor.loadStacks(machine, stackName); err != nil {
			return err
		}
		if requestedInstances, err = before.Instances(stackName, appNames); err != nil {
			return err
		}
		start, err = executor.prepareStart(before, requestedInstances, options, processByName)
		return err
	})
	if err != nil {
		return err
	}
	if err := executor.deleteProcesses(ctx, before, dropped); err != nil {
		return err
	}
	after := start.After
	isRequested := map[plan.Instance]bool{}
	for _, requestedInstance := range requestedInstances {
		isRequested[requestedInstance] = true
	}
	isRunning := func(appInstance plan.Instance) bool {
		process, isHeld := processByName[after.ProcessName(appInstance)]
		return isHeld && process.State != daemon.StateExited
	}
	if failures := executor.ensureImages(after, ctx, start.Order, func(appInstance plan.Instance) bool { return !isRunning(appInstance) }, true); len(failures) > 0 {
		return failures[0].err
	}
	actionOf := func(appInstance plan.Instance) runningAction {
		if isRequested[appInstance] {
			return replaceIfChanged
		}
		return keepRunning
	}
	needsBuild := func(appInstance plan.Instance) bool {
		return !isRunning(appInstance) || (actionOf(appInstance) == replaceIfChanged && executor.specChanged(after, appInstance, processByName))
	}
	if err := executor.buildApps(ctx, after, start.Order, needsBuild); err != nil {
		return err
	}
	isSkipped := map[plan.Instance]bool{}
	for _, appInstance := range start.Order {
		if err := ctx.Err(); err != nil {
			return err
		}
		if executor.skipsForSkippedDependency(after, appInstance, isSkipped) {
			continue
		}
		if err := executor.startInstance(ctx, after, appInstance, processByName, actionOf(appInstance)); errors.Is(err, errStoppedByAnotherCommand) {
			isSkipped[appInstance] = true
		} else if err != nil {
			return err
		}
	}
	if err := executor.replaceChangedDependents(ctx, before, after, stackName, start.Order, processByName); err != nil {
		return err
	}
	if err := executor.sendRoutes(ctx); err != nil {
		return err
	}
	executor.printURLs(after, slices.DeleteFunc(slices.Clone(requestedInstances), func(appInstance plan.Instance) bool { return isSkipped[appInstance] }))
	return nil
}

// errStoppedByAnotherCommand says that an ilot stop or ilot rm took the membership away from an instance this command was starting. The line that says so is already printed.
var errStoppedByAnotherCommand = errors.New("another ilot command stopped the app")

// skipsForSkippedDependency skips an instance whose dependency another command stopped: it would start without it.
func (executor *Executor) skipsForSkippedDependency(snapshot plan.Snapshot, appInstance plan.Instance, isSkipped map[plan.Instance]bool) bool {
	dependency, hasSkippedDependency := executor.failedDependencyOf(snapshot, appInstance, isSkipped)
	if hasSkippedDependency {
		isSkipped[appInstance] = true
		fmt.Fprintf(executor.Output, "%s does not start: %s was stopped by another ilot command\n", snapshot.ProcessName(appInstance), snapshot.ProcessName(dependency))
	}
	return hasSkippedDependency
}

// underLock runs a step that reads or changes the state. It holds the state lock for that step only: never for a build, a wait or a call that starts or stops a process.
// A step reads the process list after the lock is taken, so that it sees what a command that held the lock started.
func (executor *Executor) underLock(ctx context.Context, step func() error) (err error) {
	release, err := executor.Store.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	return step()
}

// sendRoutes reads the state again, because another command can have changed it since this one planned.
func (executor *Executor) sendRoutes(ctx context.Context) error {
	current, err := executor.loadState()
	if err != nil {
		return err
	}
	return executor.Supervisor.SetRoutes(ctx, plan.Snapshot{Workspace: executor.Workspace, State: current, Machine: executor.Machine}.Routes())
}

// replaceChangedDependents replaces each running app of the stack whose specification this start changed, for example a template that now names an app of the stack, and waits for it.
// Both specifications come from the same shell environment, so a change of the shell or of the terminal replaces nothing.
// An app that the daemon already runs with the new specification, after another command replaced it, stays as it is.
func (executor *Executor) replaceChangedDependents(ctx context.Context, before, after plan.Snapshot, stackName string, startOrder []plan.Instance, processByName map[string]daemon.Process) error {
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
		isUnchangedByThisStart := err == nil && specBefore.Hash() == specAfter.Hash()
		if isUnchangedByThisStart || process.Hash == specAfter.Hash() {
			continue
		}
		replace := func() error {
			fmt.Fprintf(executor.Output, "restarting %s: this start changed its specification\n", specAfter.Name)
			return executor.put(ctx, specAfter)
		}
		if err := executor.applyAndWait(ctx, after, wantedInstance, processByName, replace); err != nil && !errors.Is(err, errStoppedByAnotherCommand) {
			return err
		}
	}
	return nil
}

// RestartApps builds the named apps again, then restarts them. The watcher of an app runs it.
// A build that fails keeps the running app.
func (executor *Executor) RestartApps(ctx context.Context, stackName string, appNames []string) error {
	machine, err := executor.machineWithShell()
	if err != nil {
		return err
	}
	var snapshot plan.Snapshot
	var dropped []plan.Instance
	err = executor.underLock(ctx, func() (err error) {
		snapshot, dropped, err = executor.loadStacks(machine, stackName)
		return err
	})
	if err != nil {
		return err
	}
	if err := executor.deleteProcesses(ctx, snapshot, dropped); err != nil {
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
	if err := executor.buildApps(ctx, snapshot, restartedInstances, func(plan.Instance) bool { return true }); err != nil {
		return err
	}
	processByName, err := executor.Supervisor.Processes(ctx)
	if err != nil {
		return err
	}
	var shownInstances []plan.Instance
	for _, appInstance := range restartedInstances {
		err := executor.startInstance(ctx, snapshot, appInstance, processByName, restartAlways)
		if errors.Is(err, errStoppedByAnotherCommand) {
			continue
		}
		if err != nil {
			return err
		}
		shownInstances = append(shownInstances, appInstance)
	}
	executor.printURLs(snapshot, shownInstances)
	return nil
}

// RestartDaemon replaces the daemon, for example after an upgrade of ilot or a change of proxy, then starts again every app that a stack of the workspace owns.
func (executor *Executor) RestartDaemon(ctx context.Context) error {
	if executor.Supervisor.IsUp(ctx) {
		fmt.Fprintln(executor.Output, "stopping every app of every stack, and the ilot daemon")
	}
	if err := executor.Supervisor.Shutdown(ctx); err != nil {
		return err
	}
	return executor.StartAll(ctx)
}

// StartAll starts each member of each stack of the workspace that does not run, after its dependencies: what a crash of the daemon stopped comes back.
// A member that runs gets no call.
func (executor *Executor) StartAll(ctx context.Context) error {
	return executor.startMembers(ctx, plan.BaseStack, false)
}

// StartStack is StartAll for the members of one stack, and the dependencies that they need from base.
func (executor *Executor) StartStack(ctx context.Context, stackName string) error {
	return executor.startMembers(ctx, stackName, true)
}

func (executor *Executor) startMembers(ctx context.Context, stackName string, isOnlyThisStack bool) error {
	if err := executor.Supervisor.Up(ctx); err != nil {
		return err
	}
	machine, err := executor.machineWithShell()
	if err != nil {
		return err
	}
	var processByName map[string]daemon.Process
	var start plan.Start
	var problems []error
	var warnings []string
	err = executor.underLock(ctx, func() (err error) {
		if processByName, err = executor.Supervisor.Processes(ctx); err != nil {
			return err
		}
		snapshot, _, err := executor.loadStacks(machine, stackName)
		if err != nil {
			return err
		}
		onlyStack := ""
		if isOnlyThisStack {
			onlyStack = stackName
		}
		start, problems, warnings, err = executor.planMembers(snapshot, onlyStack, processByName)
		return err
	})
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		fmt.Fprintln(executor.Output, "warning: "+warning)
	}
	if len(start.Order) == 0 && len(problems) == 0 {
		if isOnlyThisStack && len(warnings) == 0 {
			fmt.Fprintf(executor.Output, "the stack %s owns no app: name the apps to start\n", stackName)
		}
		return executor.sendRoutes(ctx)
	}
	isDown := func(appInstance plan.Instance) bool {
		process, isHeld := processByName[start.After.ProcessName(appInstance)]
		return !isHeld || process.State == daemon.StateExited
	}
	imageFailures := executor.ensureImages(start.After, ctx, start.Order, isDown, false)
	if err := executor.buildApps(ctx, start.After, start.Order, isDown); err != nil {
		return err
	}
	err = executor.startEachUnlessADependencyFailed(ctx, start.After, start.Order, problems, imageFailures, processByName)
	return errors.Join(err, executor.sendRoutes(ctx))
}

// planMembers plans the start of every member of one stack, or of every stack when onlyStack is empty.
// It reads the file of each stack. A stack whose file is broken stays down, and so does an instance whose templates do not render, with its dependents. The others start.
func (executor *Executor) planMembers(snapshot plan.Snapshot, onlyStack string, processByName map[string]daemon.Process) (plan.Start, []error, []string, error) {
	var problems []error
	var warnings []string
	var loadedInstances []plan.Instance
	for _, wantedInstance := range snapshot.Wanted() {
		if onlyStack != "" && wantedInstance.Stack != onlyStack {
			continue
		}
		if err := executor.addStack(snapshot, wantedInstance.Stack); err != nil {
			problems = append(problems, err)
			continue
		}
		loadedInstances = append(loadedInstances, wantedInstance)
	}
	// The file of any stack can declare an app with the scope base, so this test waits until every file is loaded.
	var memberInstances []plan.Instance
	for _, loadedInstance := range loadedInstances {
		if _, isDeclared := snapshot.Declaration(loadedInstance); !isDeclared {
			warnings = append(warnings, fmt.Sprintf("%s does not start: no ilot.yaml declares it any more. Give back its ports with ilot stop %s in stack %s", snapshot.ProcessName(loadedInstance), loadedInstance.App, loadedInstance.Stack))
			continue
		}
		memberInstances = append(memberInstances, loadedInstance)
	}
	if len(memberInstances) == 0 {
		return plan.Start{}, problems, warnings, nil
	}
	start, err := executor.planWithPorts(snapshot, memberInstances, StartOptions{SkipDependencies: false}, processByName)
	if err != nil {
		return plan.Start{}, nil, nil, err
	}
	if err := executor.saveStart(start); err != nil {
		return plan.Start{}, nil, nil, err
	}
	var renderProblems []error
	start.Order, renderProblems = executor.withoutUnrenderable(start.After, start.Order)
	return start, append(problems, renderProblems...), warnings, nil
}

// withoutUnrenderable leaves out each instance whose specification or build does not render, and each instance that depends on one.
func (executor *Executor) withoutUnrenderable(snapshot plan.Snapshot, startOrder []plan.Instance) ([]plan.Instance, []error) {
	var renderable []plan.Instance
	var problems []error
	isLeftOut := map[plan.Instance]bool{}
	for _, appInstance := range startOrder {
		if leftOutDependency, hasLeftOutDependency := executor.failedDependencyOf(snapshot, appInstance, isLeftOut); hasLeftOutDependency {
			isLeftOut[appInstance] = true
			problems = append(problems, fmt.Errorf("%s skipped: it depends on %s", snapshot.ProcessName(appInstance), snapshot.ProcessName(leftOutDependency)))
			continue
		}
		if err := renderErrorOf(snapshot, appInstance); err != nil {
			isLeftOut[appInstance] = true
			problems = append(problems, fmt.Errorf("%s does not start: %w", snapshot.ProcessName(appInstance), err))
			continue
		}
		renderable = append(renderable, appInstance)
	}
	return renderable, problems
}

func renderErrorOf(snapshot plan.Snapshot, appInstance plan.Instance) error {
	if _, err := snapshot.Spec(appInstance); err != nil {
		return err
	}
	if build := snapshot.App(appInstance).Build; build != "" {
		_, err := snapshot.Render(appInstance, build)
		return err
	}
	return nil
}

// One app that fails must not leave the others of a restore down.
// The problems of the plan come first, so that the hint at the end covers them too.
func (executor *Executor) startEachUnlessADependencyFailed(ctx context.Context, snapshot plan.Snapshot, startOrder []plan.Instance, planProblems []error, imageFailures []imageFailure, processByName map[string]daemon.Process) error {
	problems := slices.Clone(planProblems)
	isFailed := map[plan.Instance]bool{}
	isSkipped := map[plan.Instance]bool{}
	for _, failure := range imageFailures {
		isFailed[failure.appInstance] = true
		problems = append(problems, fmt.Errorf("%s: %w", snapshot.ProcessName(failure.appInstance), failure.err))
	}
	for _, appInstance := range startOrder {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(problems, err)...)
		}
		if isFailed[appInstance] {
			continue
		}
		if failedDependency, hasFailedDependency := executor.failedDependencyOf(snapshot, appInstance, isFailed); hasFailedDependency {
			isFailed[appInstance] = true
			problems = append(problems, fmt.Errorf("%s skipped: it depends on %s", snapshot.ProcessName(appInstance), snapshot.ProcessName(failedDependency)))
			continue
		}
		if executor.skipsForSkippedDependency(snapshot, appInstance, isSkipped) {
			continue
		}
		if err := executor.startInstance(ctx, snapshot, appInstance, processByName, keepRunning); errors.Is(err, errStoppedByAnotherCommand) {
			isSkipped[appInstance] = true
		} else if err != nil {
			isFailed[appInstance] = true
			problems = append(problems, err)
		}
	}
	if len(problems) > 0 {
		problems = append(problems, errors.New("fix the cause, then run ilot start with the apps above"))
	}
	return errors.Join(problems...)
}

func (executor *Executor) failedDependencyOf(snapshot plan.Snapshot, appInstance plan.Instance, isFailed map[plan.Instance]bool) (plan.Instance, bool) {
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
func (executor *Executor) prepareStart(before plan.Snapshot, requestedInstances []plan.Instance, options StartOptions, processByName map[string]daemon.Process) (plan.Start, error) {
	start, err := executor.planWithPorts(before, requestedInstances, options, processByName)
	if err != nil {
		return plan.Start{}, err
	}
	for _, appInstance := range start.Order {
		if _, err := start.After.Spec(appInstance); err != nil {
			return plan.Start{}, err
		}
	}
	return start, executor.saveStart(start)
}

func (executor *Executor) planWithPorts(before plan.Snapshot, requestedInstances []plan.Instance, options StartOptions, processByName map[string]daemon.Process) (plan.Start, error) {
	start := before.PlanStart(requestedInstances, options.SkipDependencies)
	if err := executor.checkExternalServicesUp(start.Externals); err != nil {
		return plan.Start{}, err
	}
	if err := executor.checkStoredPorts(before, start, processByName); err != nil {
		return plan.Start{}, err
	}
	if err := executor.allocatePorts(start); err != nil {
		return plan.Start{}, err
	}
	return start, nil
}

func (executor *Executor) saveStart(start plan.Start) error {
	start.After.RecordRoutes(start.Order)
	return executor.saveStacks(start.After)
}

// What a command does with an instance that the daemon already runs.
type runningAction int

const (
	// A dependency that runs gets no call, so a change that a command did not ask for never replaces it.
	keepRunning runningAction = iota
	// ilot start converges: it replaces a named app that runs only when its specification changed.
	replaceIfChanged
	// ilot restart starts the app again even with the same specification, so that the app gets the session of this terminal, for example a new SSH_AUTH_SOCK.
	restartAlways
)

// startInstance sends one instance to the daemon and waits until it is ready.
func (executor *Executor) startInstance(ctx context.Context, snapshot plan.Snapshot, appInstance plan.Instance, processByName map[string]daemon.Process, action runningAction) error {
	spec, err := snapshot.Spec(appInstance)
	if err != nil {
		return err
	}
	process, isHeld := processByName[spec.Name]
	isRunning := isHeld && process.State != daemon.StateExited
	switch {
	case !isRunning:
		return executor.applyAndWait(ctx, snapshot, appInstance, processByName, func() error { return executor.put(ctx, spec) })
	case action == keepRunning:
		return executor.waitReady(ctx, snapshot, appInstance)
	case process.Hash != spec.Hash():
		return executor.applyAndWait(ctx, snapshot, appInstance, processByName, func() error {
			fmt.Fprintf(executor.Output, "restarting %s with its new specification\n", spec.Name)
			return executor.put(ctx, spec)
		})
	case action == replaceIfChanged:
		fmt.Fprintf(executor.Output, "%s already runs: ilot restart %s builds it again and restarts it\n", spec.Name, appInstance.App)
		return executor.waitReady(ctx, snapshot, appInstance)
	}
	return executor.applyAndWait(ctx, snapshot, appInstance, processByName, func() error {
		fmt.Fprintf(executor.Output, "restarting %s\n", spec.Name)
		return executor.Supervisor.Restart(ctx, spec)
	})
}

// specChanged tells whether the daemon runs an instance with another specification than the one of the snapshot.
func (executor *Executor) specChanged(snapshot plan.Snapshot, appInstance plan.Instance, processByName map[string]daemon.Process) bool {
	spec, err := snapshot.Spec(appInstance)
	if err != nil {
		return true
	}
	return processByName[spec.Name].Hash != spec.Hash()
}

// applyAndWait sends one instance and its watcher to the daemon, then waits until the instance is ready. An instance that is no member any more gets no call.
func (executor *Executor) applyAndWait(ctx context.Context, snapshot plan.Snapshot, appInstance plan.Instance, processByName map[string]daemon.Process, apply func() error) error {
	processName := snapshot.ProcessName(appInstance)
	if isWanted, err := executor.isStillWanted(ctx, appInstance); err != nil || !isWanted {
		if err == nil {
			fmt.Fprintf(executor.Output, "%s does not start: another ilot command stopped it\n", processName)
			err = errStoppedByAnotherCommand
		}
		return err
	}
	if err := apply(); err != nil {
		return err
	}
	if err := executor.putWatcher(ctx, snapshot, appInstance, processByName); err != nil {
		return err
	}
	// A stop between the test above and the start found no process to stop, so this command stops it.
	if isWanted, err := executor.isStillWanted(ctx, appInstance); err != nil || !isWanted {
		if err == nil {
			fmt.Fprintf(executor.Output, "%s stops again: another ilot command stopped it while it started\n", processName)
			if err = executor.deleteProcesses(ctx, snapshot, []plan.Instance{appInstance}); err == nil {
				err = errStoppedByAnotherCommand
			}
		}
		return err
	}
	return executor.waitReady(ctx, snapshot, appInstance)
}

// isStillWanted reads the state again: an ilot stop or an ilot rm can have taken the membership away while this command built.
func (executor *Executor) isStillWanted(ctx context.Context, appInstance plan.Instance) (isWanted bool, err error) {
	err = executor.underLock(ctx, func() error {
		current, err := executor.loadState()
		isWanted = current.App(executor.Workspace, appInstance.Stack, appInstance.App).Wanted
		return err
	})
	return isWanted, err
}

func (executor *Executor) put(ctx context.Context, spec plan.ProcessSpec) error {
	if err := os.MkdirAll(filepath.Dir(spec.LogFile), 0o700); err != nil {
		return err
	}
	return executor.Supervisor.Put(ctx, spec)
}

// The watcher of an app restarts only when its own specification changed: it runs ilot restart, which rebuilds the app itself.
func (executor *Executor) putWatcher(ctx context.Context, snapshot plan.Snapshot, appInstance plan.Instance, processByName map[string]daemon.Process) error {
	watcher, isWatched, err := snapshot.Watcher(appInstance)
	if err != nil || !isWatched {
		return err
	}
	if process, isHeld := processByName[watcher.Name]; isHeld && process.State != daemon.StateExited && process.Hash == watcher.Hash() {
		return nil
	}
	return executor.put(ctx, watcher)
}

func (executor *Executor) waitReady(ctx context.Context, snapshot plan.Snapshot, appInstance plan.Instance) error {
	processName := snapshot.ProcessName(appInstance)
	fmt.Fprintf(executor.Output, "waiting for %s\n", processName)
	stopShowingLog := logs.FollowNewMessages(snapshot.LogFile(appInstance), &linePrefixWriter{prefix: processName + " | ", output: executor.Output, isAtLineStart: true})
	waitErr := executor.Supervisor.WaitReady(ctx, processName, executor.ReadyTimeout)
	if err := errors.Join(waitErr, stopShowingLog()); err != nil {
		if errors.Is(err, daemon.ErrUnknownProcess) {
			if isWanted, wantedErr := executor.isStillWanted(ctx, appInstance); wantedErr == nil && !isWanted {
				fmt.Fprintf(executor.Output, "%s does not start: another ilot command stopped it\n", processName)
				return errStoppedByAnotherCommand
			}
		}
		failure := fmt.Errorf("%w, see %s", err, snapshot.LogFile(appInstance))
		if _, isNotReadyInTime := errors.AsType[*daemon.NotReadyError](err); isNotReadyInTime {
			return fmt.Errorf("%w; it still starts: ilot keeps it", failure)
		}
		return failure
	}
	return nil
}

// Silent builds (go build) otherwise look like ilot hangs.
func (executor *Executor) printProgressUntilStopped(task string) (stop func()) {
	if executor.ProgressInterval <= 0 {
		return func() {}
	}
	startedAt := time.Now()
	stopRequested := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(executor.ProgressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopRequested:
				return
			case <-ticker.C:
				fmt.Fprintf(executor.Output, "still %s, %s\n", task, time.Since(startedAt).Round(time.Second))
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

func (executor *Executor) Stop(ctx context.Context, stackName string, appNames []string) error {
	return executor.stop(ctx, stackName, func(current state.State, _ map[string]daemon.Process) ([]plan.Instance, error) {
		return executor.instancesToStop(current, stackName, appNames)
	})
}

// StopStack stops every app that a stack owns, and each app of the stack that the daemon still holds. It stops no dependency in another stack.
func (executor *Executor) StopStack(ctx context.Context, stackName string) error {
	return executor.stop(ctx, stackName, func(current state.State, processByName map[string]daemon.Process) ([]plan.Instance, error) {
		var instances []plan.Instance
		apps := current.Stacks(executor.Workspace)[stackName].Apps
		for _, appName := range slices.Sorted(maps.Keys(apps)) {
			appInstance := plan.Instance{Stack: stackName, App: appName}
			if _, isHeld := processByName[plan.ProcessName(executor.Workspace, appInstance)]; isHeld || apps[appName].Wanted {
				instances = append(instances, appInstance)
			}
		}
		if len(instances) == 0 {
			fmt.Fprintf(executor.Output, "the stack %s runs no app\n", stackName)
		}
		return instances, nil
	})
}

func (executor *Executor) stop(ctx context.Context, stackName string, instancesOf func(state.State, map[string]daemon.Process) ([]plan.Instance, error)) error {
	var snapshot plan.Snapshot
	var stopped []plan.Instance
	err := executor.underLock(ctx, func() error {
		processByName, err := executor.Supervisor.Processes(ctx)
		if err != nil {
			return err
		}
		snapshot, stopped, err = executor.planStop(stackName, instancesOf, processByName)
		return err
	})
	if err != nil {
		return err
	}
	if err := executor.deleteProcesses(ctx, snapshot, stopped); err != nil {
		return err
	}
	for _, stoppedInstance := range stopped {
		fmt.Fprintf(executor.Output, "stopped %s\n", snapshot.ProcessName(stoppedInstance))
	}
	return executor.sendRoutes(ctx)
}

// planStop takes the membership away from the stopped instances. An app that no ilot.yaml declares any more leaves the state, and gives its ports back.
// An instance that is no member but that the daemon holds is stopped too: a start that raced an earlier stop can have left it.
// The snapshot that it gives is the state before the stop, which still names the container of each instance.
func (executor *Executor) planStop(stackName string, instancesOf func(state.State, map[string]daemon.Process) ([]plan.Instance, error), processByName map[string]daemon.Process) (plan.Snapshot, []plan.Instance, error) {
	current, err := executor.loadState()
	if err != nil {
		return plan.Snapshot{}, nil, err
	}
	stoppedInstances, err := instancesOf(current, processByName)
	if err != nil {
		return plan.Snapshot{}, nil, err
	}
	// The warning about dependents and the test of the declarations need the configuration. Without it, the stop still works.
	before, configErr := executor.snapshotOf(current.Clone(), stackName)
	if configErr != nil {
		before = plan.Snapshot{Workspace: executor.Workspace, Own: stackName, State: current.Clone(), Machine: executor.Machine}
	}
	snapshot := plan.Snapshot{Workspace: executor.Workspace, Own: stackName, State: current, Machine: executor.Machine}
	var stopped []plan.Instance
	for _, stoppedInstance := range stoppedInstances {
		if _, isDeclared := before.Declaration(stoppedInstance); configErr == nil && !isDeclared {
			fmt.Fprintf(executor.Output, "%s: no ilot.yaml declares it, so it leaves the stack and gives back its ports\n", snapshot.ProcessName(stoppedInstance))
			delete(current.Stack(executor.Workspace, stoppedInstance.Stack).Apps, stoppedInstance.App)
			stopped = append(stopped, stoppedInstance)
			continue
		}
		if !snapshot.IsWanted(stoppedInstance) {
			if _, isHeld := processByName[snapshot.ProcessName(stoppedInstance)]; isHeld {
				stopped = append(stopped, stoppedInstance)
			} else {
				fmt.Fprintf(executor.Output, "%s is not running\n", snapshot.ProcessName(stoppedInstance))
			}
			continue
		}
		snapshot.SetWanted(stoppedInstance, false)
		stopped = append(stopped, stoppedInstance)
		if configErr == nil {
			executor.warnAboutDependents(before, stoppedInstance, stoppedInstances)
		}
	}
	return before, stopped, executor.saveStacks(snapshot)
}

// deleteProcesses stops instances and their watchers. A container that the daemon does not hold, after a crash of the daemon, is removed too.
func (executor *Executor) deleteProcesses(ctx context.Context, snapshot plan.Snapshot, instances []plan.Instance) error {
	if len(instances) == 0 {
		return nil
	}
	processByName, err := executor.Supervisor.Processes(ctx)
	if err != nil {
		return err
	}
	for _, deletedInstance := range instances {
		processName := snapshot.ProcessName(deletedInstance)
		watcherName := snapshot.ProcessName(plan.Instance{Stack: deletedInstance.Stack, App: deletedInstance.App + plan.WatcherSuffix})
		for _, name := range []string{watcherName, processName} {
			if err := executor.Supervisor.Delete(ctx, name); err != nil {
				return err
			}
		}
		containerName := snapshot.State.App(executor.Workspace, deletedInstance.Stack, deletedInstance.App).Container
		if _, isHeld := processByName[processName]; containerName != "" && !isHeld && executor.RemoveContainer != nil {
			if err := executor.RemoveContainer(ctx, containerName); err != nil {
				return fmt.Errorf("remove the container %s of %s: %w", containerName, processName, err)
			}
		}
	}
	return nil
}

// A dependent that the same command stops gets no warning.
func (executor *Executor) warnAboutDependents(snapshotBeforeStop plan.Snapshot, stoppedInstance plan.Instance, stoppedByThisCommand []plan.Instance) {
	for _, runningInstance := range snapshotBeforeStop.Wanted() {
		if slices.Contains(stoppedByThisCommand, runningInstance) || !slices.Contains(snapshotBeforeStop.App(runningInstance).DependsOn, stoppedInstance.App) {
			continue
		}
		if snapshotBeforeStop.Serving(runningInstance.Stack, stoppedInstance.App) == stoppedInstance.Stack {
			fmt.Fprintf(executor.Output, "warning: %s depends on %s and keeps running\n", plan.ProcessName(executor.Workspace, runningInstance), plan.ProcessName(executor.Workspace, stoppedInstance))
		}
	}
}
