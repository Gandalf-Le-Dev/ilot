package devctl

import (
	"strings"
	"testing"
)

func TestCommandsOfARepoWithMiseRunThroughMise(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.UsesMise = func(checkoutDir string) bool { return checkoutDir == "/src/portal" }

	environment.start(t, environment.master, StartOptions{}, "portal-standalone")

	if want := (recordedBuild{workingDir: "/src/portal", command: "mise exec -- bash -c 'go build ./cmd/api'"}); len(*environment.builds) != 1 || (*environment.builds)[0] != want {
		t.Errorf("builds = %+v, want %+v", *environment.builds, want)
	}
	runCommand := environment.supervisor.runningSpecs["portal-standalone.master"].Command
	if !strings.HasPrefix(runCommand, "mise exec -- bash -c 'api --port=") || !strings.HasSuffix(runCommand, " --from=/src/portal'") {
		t.Errorf("run command = %q, want the run command inside mise exec", runCommand)
	}
}

func TestCommandsOfARepoWithoutMiseRunAsTheyAre(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.controller.UsesMise = func(checkoutDir string) bool { return checkoutDir == "/src/portal" }

	environment.start(t, environment.master, StartOptions{}, "standalone")

	if runCommand := environment.supervisor.runningSpecs["standalone.master"].Command; !strings.HasPrefix(runCommand, "standalone --port=") {
		t.Errorf("run command = %q, want it unchanged", runCommand)
	}
}
