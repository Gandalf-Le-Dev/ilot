package main

import "testing"

func TestRoutesDirFromMountsFindsTheTraefikRoutesBindSource(t *testing.T) {
	mountsJSON := []byte(`[
		{"Type":"bind","Source":"/var/run/docker.sock","Destination":"/var/run/docker.sock"},
		{"Type":"bind","Source":"/home/user/devbox/platform/devbox/ilotproxy/routes","Destination":"/etc/traefik/routes"}
	]`)

	routesDir, err := routesDirFromMounts(mountsJSON)
	if err != nil {
		t.Fatalf("routesDirFromMounts: %v", err)
	}
	if routesDir != "/home/user/devbox/platform/devbox/ilotproxy/routes" {
		t.Errorf("routesDir = %q", routesDir)
	}
}

func TestRoutesDirFromMountsFailsWithoutTheRoutesMount(t *testing.T) {
	if _, err := routesDirFromMounts([]byte(`[]`)); err == nil {
		t.Fatal("want an error when no mount targets /etc/traefik/routes")
	}
}
