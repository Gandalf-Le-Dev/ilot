package engine

import (
	"context"
	"log/slog"
	"maps"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

var discardLogger = slog.New(slog.DiscardHandler)

// audit gives the audit log of the command: one record for each port, each change of the state and each call to the daemon.
func (executor *Executor) audit() *slog.Logger {
	if executor.Logger == nil {
		return discardLogger
	}
	return executor.Logger
}

// auditStateChanges records what a save changed in the stacks of the workspace. The ports have their own records.
func (executor *Executor) auditStateChanges(before, after state.State) {
	beforeStacks, afterStacks := before.Stacks(executor.Workspace), after.Stacks(executor.Workspace)
	stackNames := slices.Sorted(maps.Keys(beforeStacks))
	for stackName := range afterStacks {
		if !slices.Contains(stackNames, stackName) {
			stackNames = append(stackNames, stackName)
		}
	}
	slices.Sort(stackNames)
	for _, stackName := range stackNames {
		beforeStack, wasKnown := beforeStacks[stackName]
		afterStack, isKnown := afterStacks[stackName]
		if wasKnown && !isKnown {
			executor.audit().Info("stack removed", "changed_stack", stackName)
			continue
		}
		var beforeApps map[string]state.App
		if wasKnown {
			beforeApps = beforeStack.Apps
		}
		for _, appName := range slices.Sorted(maps.Keys(afterStack.Apps)) {
			process := plan.ProcessName(executor.Workspace, plan.Instance{Stack: stackName, App: appName})
			if wanted := afterStack.Apps[appName].Wanted; beforeApps[appName].Wanted != wanted {
				executor.audit().Info("membership changed", "process", process, "wanted", wanted)
			}
		}
		for appName := range beforeApps {
			if _, isKept := afterStack.Apps[appName]; !isKept {
				executor.audit().Info("app left the state", "process", plan.ProcessName(executor.Workspace, plan.Instance{Stack: stackName, App: appName}))
			}
		}
	}
}

// AuditedSupervisor writes one record of the audit log for each call that starts, stops or routes a process.
type AuditedSupervisor struct {
	ProcessSupervisor
	Logger *slog.Logger
}

func (supervisor AuditedSupervisor) record(call, processName string, err error, attributes ...any) {
	attributes = append([]any{"call", call, "process", processName}, attributes...)
	if err != nil {
		supervisor.Logger.Warn("daemon call failed", append(attributes, "error", err.Error())...)
		return
	}
	supervisor.Logger.Info("daemon call", attributes...)
}

func (supervisor AuditedSupervisor) Put(ctx context.Context, spec ProcessSpec) error {
	err := supervisor.ProcessSupervisor.Put(ctx, spec)
	supervisor.record("put", spec.Name, err, "hash", spec.Hash())
	return err
}

func (supervisor AuditedSupervisor) Restart(ctx context.Context, spec ProcessSpec) error {
	err := supervisor.ProcessSupervisor.Restart(ctx, spec)
	supervisor.record("restart", spec.Name, err, "hash", spec.Hash())
	return err
}

func (supervisor AuditedSupervisor) Delete(ctx context.Context, processName string) error {
	err := supervisor.ProcessSupervisor.Delete(ctx, processName)
	supervisor.record("delete", processName, err)
	return err
}

func (supervisor AuditedSupervisor) SetRoutes(ctx context.Context, routes []daemon.Route) error {
	err := supervisor.ProcessSupervisor.SetRoutes(ctx, routes)
	supervisor.record("routes", "", err, "routes", len(routes))
	return err
}

func (supervisor AuditedSupervisor) Shutdown(ctx context.Context) error {
	err := supervisor.ProcessSupervisor.Shutdown(ctx)
	supervisor.record("shutdown", "", err)
	return err
}
