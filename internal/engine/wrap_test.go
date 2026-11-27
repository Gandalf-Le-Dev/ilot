package engine

import (
	"strings"
	"testing"
)

func TestCommandsOfARepoWithAWrapRunBehindItsPrefix(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Config.RepoWrap = map[string]string{"studio": "mise exec --"}
	studioStandalone := environment.executor.Config.Apps["studio-standalone"]
	studioStandalone.Stop = "api stop --port={{.Ports.http}}"
	environment.executor.Config.Apps["studio-standalone"] = studioStandalone

	environment.start(t, environment.base, StartOptions{}, "studio-standalone")

	if want := (recordedBuild{workingDir: "/src/studio", command: "mise exec -- sh -c 'go build ./cmd/api'"}); len(*environment.builds) != 1 || (*environment.builds)[0] != want {
		t.Errorf("builds = %+v, want %+v", *environment.builds, want)
	}
	spec := environment.supervisor.runningSpecs["studio-standalone.base.shop"]
	if !strings.HasPrefix(spec.Command, "mise exec -- sh -c 'api --port=") || !strings.HasSuffix(spec.Command, " --from=/src/studio'") {
		t.Errorf("run command = %q, want the run command behind the prefix", spec.Command)
	}
	if !strings.HasPrefix(spec.Stop, "mise exec -- sh -c 'api stop --port=") {
		t.Errorf("stop command = %q, want the stop command behind the prefix", spec.Stop)
	}
}

func TestCommandsOfARepoWithoutAWrapRunAsTheyAre(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.executor.Config.RepoWrap = map[string]string{"studio": "mise exec --"}

	environment.start(t, environment.base, StartOptions{}, "standalone")

	if runCommand := environment.supervisor.runningSpecs["standalone.base.shop"].Command; !strings.HasPrefix(runCommand, "standalone --port=") {
		t.Errorf("run command = %q, want it unchanged: the wrap belongs to another repo", runCommand)
	}
}
