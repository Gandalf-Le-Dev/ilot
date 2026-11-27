package ilotctl

import (
	"errors"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/appconfig"
)

func TestCheckRenderingAcceptsAValidConfig(t *testing.T) {
	if problems := CheckRendering(testConfig()); len(problems) != 0 {
		t.Fatalf("CheckRendering = %v, want no problem", errors.Join(problems...))
	}
}

func TestCheckRenderingReportsATemplateTypoWithItsApp(t *testing.T) {
	config := testConfig()
	config.Apps["typo-api"] = appconfig.App{Run: `api --port={{port "gateway" "htp"}}`}

	problems := CheckRendering(config)

	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "typo-api") || !strings.Contains(problems[0].Error(), "htp") {
		t.Fatalf("CheckRendering = %v, want one problem naming typo-api and htp", errors.Join(problems...))
	}
}

func TestCheckRenderingReportsATypoInAStopCommand(t *testing.T) {
	config := testConfig()
	config.Apps["stoppable"] = appconfig.App{Run: "docker run --name stoppable nginx", Stop: "docker stop {{.Stak}}"}

	problems := CheckRendering(config)

	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "stoppable") || !strings.Contains(problems[0].Error(), "Stak") {
		t.Fatalf("CheckRendering = %v, want one problem naming stoppable and Stak", errors.Join(problems...))
	}
}

func TestCheckRenderingReportsAnUnknownRepo(t *testing.T) {
	config := testConfig()
	config.Apps["mounting"] = appconfig.App{
		Container: appconfig.Container{Image: "registry/mounting:latest", Ports: map[string]int{"http": 8080}, Mounts: map[string]string{`{{repo "nowhere"}}`: "/src"}},
	}

	problems := CheckRendering(config)

	if len(problems) == 0 || !strings.Contains(errors.Join(problems...).Error(), "nowhere") {
		t.Fatalf("CheckRendering = %v, want a problem naming the unknown repo", errors.Join(problems...))
	}
}

func TestCheckRenderingReportsEveryBrokenTemplateOfAnApp(t *testing.T) {
	config := testConfig()
	config.Apps["many-typos"] = appconfig.App{
		Env: map[string]string{"GOOD": "{{.Ports.http}}", "FIRST": `{{port "ghost" "http"}}`, "SECOND": "{{.Ports.nope}}"},
		Container: appconfig.Container{Image: "registry/many:latest", Ports: map[string]int{"http": 8080}, Mounts: map[string]string{
			`{{repo "nowhere"}}`:   "/a",
			`{{repo "elsewhere"}}`: "/b",
		}, Args: []string{"--label={{.NoSuchField}}"}},
	}

	problems := CheckRendering(config)

	if len(problems) != 5 {
		t.Fatalf("CheckRendering = %d problems, want 5 (two env values, two mounts, one flag of docker run):\n%v", len(problems), errors.Join(problems...))
	}
	for _, problem := range problems {
		if !strings.HasPrefix(problem.Error(), "many-typos: ") {
			t.Errorf("problem %q does not start with its app", problem)
		}
	}
}

func TestCheckRenderingTagsEachProblemWithTheRepoOfItsApp(t *testing.T) {
	config := testConfig()
	config.Apps["portal-typo"] = appconfig.App{Repo: "portal", Run: `api --port={{port "gateway" "htp"}}`}

	problems := CheckRendering(config)

	var renderProblem RenderProblem
	if len(problems) != 1 || !errors.As(problems[0], &renderProblem) || renderProblem.Repo != "portal" {
		t.Fatalf("CheckRendering = %v, want one RenderProblem of repo portal", errors.Join(problems...))
	}
}
