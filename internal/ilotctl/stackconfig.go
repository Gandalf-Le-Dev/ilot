package ilotctl

import (
	"fmt"
	"maps"
	"slices"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
	"github.com/Gandalf-Le-Dev/ilot/internal/state"
)

// A stack whose config cannot be read keeps the config of the command, so a listing still shows it.
func (controller *Controller) loadStackConfigs(stackNames []string) (map[string]config.Config, []error) {
	configByStack := map[string]config.Config{}
	if controller.ConfigForStack == nil {
		return configByStack, nil
	}
	var problems []error
	for _, stackName := range stackNames {
		stackConfig, err := controller.ConfigForStack(stackName)
		if err != nil {
			problems = append(problems, fmt.Errorf("read the config of stack %s: %w: fix it, or remove the stack with ilot rm %s", stackName, err, stackName))
			continue
		}
		configByStack[stackName] = stackConfig
	}
	return configByStack, problems
}

func (controller *Controller) snapshotOf(current state.State, ownStackName string) (plan.Snapshot, []error) {
	stackNames := slices.Sorted(maps.Keys(current.Stacks(controller.Workspace)))
	configByStack, problems := controller.loadStackConfigs(stackNames)
	configByStack[ownStackName] = controller.Config
	return plan.Snapshot{
		Workspace:      controller.Workspace,
		Own:            ownStackName,
		State:          current,
		Configs:        configByStack,
		Checkouts:      map[string]map[string]string{},
		ReposNotCloned: controller.ReposNotCloned,
	}, problems
}

func (controller *Controller) prepareStacks(snapshot plan.Snapshot) {
	for _, wantedInstance := range snapshot.Wanted() {
		if _, isDeclared := snapshot.Declaration(wantedInstance); !isDeclared {
			snapshot.SetWanted(wantedInstance, false)
			fmt.Fprintf(controller.Output, "warning: %s stopped: no ilot.yaml of stack %s declares it any more\n", controller.processName(wantedInstance), wantedInstance.Stack)
		}
	}
}

func repoEnvOf(snapshot plan.Snapshot, appInstance plan.Instance) map[string]map[string]string {
	declaringConfig, _ := snapshot.DeclaringConfig(appInstance)
	return declaringConfig.RepoEnv
}
