package ilotctl

import (
	"strings"
	"testing"
)

func TestCommandsOfARepoWithAWrapRunBehindItsPrefix(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.Config.RepoWrap = map[string]string{"portal": "mise exec --"}
	portalStandalone := environment.controller.Config.Apps["portal-standalone"]
	portalStandalone.Stop = "api stop --port={{.Ports.http}}"
	environment.controller.Config.Apps["portal-standalone"] = portalStandalone

	environment.start(t, environment.master, StartOptions{}, "portal-standalone")

	if want := (recordedBuild{workingDir: "/src/portal", command: "mise exec -- sh -c 'go build ./cmd/api'"}); len(*environment.builds) != 1 || (*environment.builds)[0] != want {
		t.Errorf("builds = %+v, want %+v", *environment.builds, want)
	}
	spec := environment.supervisor.runningSpecs["portal-standalone.master"]
	if !strings.HasPrefix(spec.Command, "mise exec -- sh -c 'api --port=") || !strings.HasSuffix(spec.Command, " --from=/src/portal'") {
		t.Errorf("run command = %q, want the run command behind the prefix", spec.Command)
	}
	if !strings.HasPrefix(spec.Stop, "mise exec -- sh -c 'api stop --port=") {
		t.Errorf("stop command = %q, want the stop command behind the prefix", spec.Stop)
	}
}

func TestCommandsOfARepoWithoutAWrapRunAsTheyAre(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.Config.RepoWrap = map[string]string{"portal": "mise exec --"}

	environment.start(t, environment.master, StartOptions{}, "standalone")

	if runCommand := environment.supervisor.runningSpecs["standalone.master"].Command; !strings.HasPrefix(runCommand, "standalone --port=") {
		t.Errorf("run command = %q, want it unchanged: the wrap belongs to another repo", runCommand)
	}
}
