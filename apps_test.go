package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
)

func TestDeclaredServicesListEveryAppWithItsRepoAndDependenciesThenTheExternalServices(t *testing.T) {
	config := config.Config{
		Apps: map[string]config.App{
			"publicapi":  {Repo: "payments", DependsOn: []string{"router", "secrets"}},
			"router":     {Repo: "payments"},
			"studio-api": {Repo: "studio", DependsOn: []string{"scheduler"}},
		},
		External: map[string]config.ExternalService{"secrets": {Port: 8200}},
	}

	encoded, err := json.Marshal(declaredServicesOf("task-0001", config))

	if err != nil {
		t.Fatal(err)
	}
	want := `{"stack":"task-0001",` +
		`"apps":[{"name":"publicapi","repo":"payments","depends_on":["router","secrets"]},{"name":"router","repo":"payments","depends_on":[]},{"name":"studio-api","repo":"studio","depends_on":["scheduler"]}],` +
		`"external_services":[{"name":"secrets","port":8200}]}`
	if string(encoded) != want {
		t.Errorf("JSON =\n%s\nwant\n%s", encoded, want)
	}
}

func TestDeclaredServicesTableShowsTheAppsThenTheExternalServices(t *testing.T) {
	services := declaredServices{
		Stack:            "base",
		Apps:             []declaredApp{{Name: "router", Repo: "payments", DependsOn: []string{}}, {Name: "publicapi", Repo: "payments", DependsOn: []string{"router", "secrets"}}},
		ExternalServices: []declaredExternalService{{Name: "secrets", Port: 8200}},
	}
	var output bytes.Buffer

	if err := printDeclaredServices(&output, services); err != nil {
		t.Fatal(err)
	}

	want := "stack: base\n\n" +
		"APP        REPO      DEPENDS ON\n" +
		"router     payments  \n" +
		"publicapi  payments  router, secrets\n" +
		"\n" +
		"EXTERNAL  PORT\n" +
		"secrets   8200\n"
	if output.String() != want {
		t.Errorf("table =\n%q\nwant\n%q", output.String(), want)
	}
}

func TestDeclaredServicesTableLeavesOutTheExternalTableWhenNoneIsDeclared(t *testing.T) {
	services := declaredServices{Stack: "base", Apps: []declaredApp{{Name: "router", Repo: "payments", DependsOn: []string{}}}, ExternalServices: []declaredExternalService{}}
	var output bytes.Buffer

	if err := printDeclaredServices(&output, services); err != nil {
		t.Fatal(err)
	}

	want := "stack: base\n\n" +
		"APP     REPO      DEPENDS ON\n" +
		"router  payments  \n"
	if output.String() != want {
		t.Errorf("table =\n%q\nwant\n%q", output.String(), want)
	}
}
