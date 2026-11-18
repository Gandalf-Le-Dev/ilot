package main

import (
	"encoding/json"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/checkouts"
	"github.com/Gandalf-Le-Dev/ilot/internal/ilotctl"
)

func TestLsJSONOutputIsOneObjectWithHomeAppsExternalServicesAndCheckouts(t *testing.T) {
	output := lsJSONOutput{
		Home: "http://home.dev.example.net:18080",
		Listing: ilotctl.Listing{
			Apps:             []ilotctl.AppRow{{Stack: "proj-0001", App: "publicapi", Status: "Running", Ports: map[string]int{"http": 10003}, URL: "http://publicapi.proj-0001.dev.example.net:18080"}},
			ExternalServices: []ilotctl.ExternalServiceRow{{Name: "keyring", Status: "up", Port: 8200}},
		},
		CheckoutsByStack: map[string]map[string]checkouts.Checkout{
			"proj-0001": {"portal": {Repo: "portal", Dir: "/home/user/worktrees-portal-0001", Branch: "PROJ-0001-x", IsMainCheckout: false}},
		},
	}

	encoded, err := json.Marshal(output)

	if err != nil {
		t.Fatal(err)
	}
	want := `{"home":"http://home.dev.example.net:18080",` +
		`"apps":[{"stack":"proj-0001","app":"publicapi","status":"Running","ports":{"http":10003},"url":"http://publicapi.proj-0001.dev.example.net:18080"}],` +
		`"external_services":[{"name":"keyring","status":"up","port":8200}],` +
		`"checkouts":{"proj-0001":{"portal":{"repo":"portal","dir":"/home/user/worktrees-portal-0001","branch":"PROJ-0001-x","is_main_checkout":false}}}}`
	if string(encoded) != want {
		t.Errorf("JSON =\n%s\nwant\n%s", encoded, want)
	}
}
