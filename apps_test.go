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
			"publicapi":  {Repo: "platform", DependsOn: []string{"gateway", "keyring"}},
			"gateway":    {Repo: "platform"},
			"portal-api": {Repo: "portal", DependsOn: []string{"temporal"}},
		},
		External: map[string]config.ExternalService{"keyring": {Port: 8200}},
	}

	encoded, err := json.Marshal(declaredServicesOf("proj-0001", config))

	if err != nil {
		t.Fatal(err)
	}
	want := `{"stack":"proj-0001",` +
		`"apps":[{"name":"gateway","repo":"platform","depends_on":[]},{"name":"portal-api","repo":"portal","depends_on":["temporal"]},{"name":"publicapi","repo":"platform","depends_on":["gateway","keyring"]}],` +
		`"external_services":[{"name":"keyring","port":8200}]}`
	if string(encoded) != want {
		t.Errorf("JSON =\n%s\nwant\n%s", encoded, want)
	}
}

func TestDeclaredServicesTableShowsTheAppsThenTheExternalServices(t *testing.T) {
	services := declaredServices{
		Stack:            "master",
		Apps:             []declaredApp{{Name: "gateway", Repo: "platform", DependsOn: []string{}}, {Name: "publicapi", Repo: "platform", DependsOn: []string{"gateway", "keyring"}}},
		ExternalServices: []declaredExternalService{{Name: "keyring", Port: 8200}},
	}
	var output bytes.Buffer

	if err := printDeclaredServices(&output, services); err != nil {
		t.Fatal(err)
	}

	want := "stack: master\n\n" +
		"APP        REPO      DEPENDS ON\n" +
		"gateway    platform  \n" +
		"publicapi  platform  gateway, keyring\n" +
		"\n" +
		"EXTERNAL  PORT\n" +
		"keyring   8200\n"
	if output.String() != want {
		t.Errorf("table =\n%q\nwant\n%q", output.String(), want)
	}
}

func TestDeclaredServicesTableLeavesOutTheExternalTableWhenNoneIsDeclared(t *testing.T) {
	services := declaredServices{Stack: "master", Apps: []declaredApp{{Name: "gateway", Repo: "platform", DependsOn: []string{}}}, ExternalServices: []declaredExternalService{}}
	var output bytes.Buffer

	if err := printDeclaredServices(&output, services); err != nil {
		t.Fatal(err)
	}

	want := "stack: master\n\n" +
		"APP      REPO      DEPENDS ON\n" +
		"gateway  platform  \n"
	if output.String() != want {
		t.Errorf("table =\n%q\nwant\n%q", output.String(), want)
	}
}
