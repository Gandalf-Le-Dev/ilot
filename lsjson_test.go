package main

import (
	"encoding/json"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/engine"
	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
)

func TestLsJSONOutputIsOneObjectWithHomeAppsExternalServicesAndCheckouts(t *testing.T) {
	output := lsJSONOutput{
		Home: "http://home.dev.example.net:18080",
		Listing: engine.Listing{
			Apps:             []engine.AppRow{{Stack: "task-0001", App: "publicapi", Status: "Running", Ports: map[string]int{"http": 10003}, URL: "http://publicapi.task-0001.dev.example.net:18080"}},
			ExternalServices: []engine.ExternalServiceRow{{Name: "secrets", Status: "up", Port: 8200}},
		},
		CheckoutsByStack: map[string]map[string]workspace.Worktree{
			"task-0001": {"studio": {Repo: "studio", Dir: "/home/user/worktrees-studio-0001", Branch: "TASK-0001-x", IsMain: false}},
		},
	}

	encoded, err := json.Marshal(output)

	if err != nil {
		t.Fatal(err)
	}
	want := `{"home":"http://home.dev.example.net:18080",` +
		`"apps":[{"stack":"task-0001","app":"publicapi","status":"Running","ports":{"http":10003},"url":"http://publicapi.task-0001.dev.example.net:18080"}],` +
		`"external_services":[{"name":"secrets","status":"up","port":8200}],` +
		`"checkouts":{"task-0001":{"studio":{"repo":"studio","dir":"/home/user/worktrees-studio-0001","branch":"TASK-0001-x","is_main_checkout":false}}}}`
	if string(encoded) != want {
		t.Errorf("JSON =\n%s\nwant\n%s", encoded, want)
	}
}
