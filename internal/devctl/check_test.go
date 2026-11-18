package devctl

import (
	"errors"
	"strings"
	"testing"

	"dev/internal/appconfig"
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

func TestCheckRenderingReportsAnUnknownRepo(t *testing.T) {
	config := testConfig()
	config.Apps["mounting"] = appconfig.App{
		Ports:      []string{"http"},
		FixedPorts: map[string]int{"http": 8080},
		Container:  appconfig.Container{Image: "registry/mounting:latest", Mounts: map[string]string{"/src": `{{repo "nowhere"}}`}},
	}

	problems := CheckRendering(config)

	if len(problems) == 0 || !strings.Contains(errors.Join(problems...).Error(), "nowhere") {
		t.Fatalf("CheckRendering = %v, want a problem naming the unknown repo", errors.Join(problems...))
	}
}

func TestCheckRenderingReportsEveryBrokenTemplateOfAnApp(t *testing.T) {
	config := testConfig()
	config.Apps["many-typos"] = appconfig.App{
		Ports:      []string{"http"},
		FixedPorts: map[string]int{"http": 8080},
		Env:        map[string]string{"GOOD": "{{.Ports.http}}", "FIRST": `{{port "ghost" "http"}}`, "SECOND": "{{.Ports.nope}}"},
		Container: appconfig.Container{Image: "registry/many:latest", Mounts: map[string]string{
			"/a": `{{repo "nowhere"}}`,
			"/b": `{{repo "elsewhere"}}`,
		}},
	}

	problems := CheckRendering(config)

	if len(problems) != 4 {
		t.Fatalf("CheckRendering = %d problems, want 4 (two env values, two mounts):\n%v", len(problems), errors.Join(problems...))
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
