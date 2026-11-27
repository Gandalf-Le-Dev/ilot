package ilotctl

import (
	"errors"
	"strings"
	"testing"
)

func TestRegistryOfImage(t *testing.T) {
	for image, want := range map[string]string{
		"registry.example.net/catalog-cache:latest": "registry.example.net",
		"localhost:5000/x":                          "localhost:5000",
		"localhost/x":                               "localhost",
		"postgres:16":                               "docker.io",
		"library/postgres":                          "docker.io",
	} {
		if got := registryOfImage(image); got != want {
			t.Errorf("registryOfImage(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestImagePullHintNamesTheLogin(t *testing.T) {
	for _, marker := range []string{"Authentication is required", "unauthorized", "no basic auth credentials", "denied: requested access"} {
		got := imagePullHint("registry.example.net/app:1", "Error response: "+marker)
		want := "log in to the registry, then run the command again: docker login registry.example.net"
		if got != want {
			t.Errorf("imagePullHint with %q = %q, want %q", marker, got, want)
		}
	}
}

func TestImagePullHintNamesDockerHubLoginWithoutARegistry(t *testing.T) {
	got := imagePullHint("postgres:16", "unauthorized")
	if want := "log in to the registry, then run the command again: docker login"; got != want {
		t.Errorf("imagePullHint = %q, want %q", got, want)
	}
}

func TestImagePullHintIsEmptyForOtherErrors(t *testing.T) {
	if got := imagePullHint("registry.example.net/app:1", "manifest unknown"); got != "" {
		t.Errorf("imagePullHint = %q, want empty", got)
	}
}

func TestImagePullErrorHoldsTheImageAndTheHint(t *testing.T) {
	err := imagePullError("registry.example.net/app:1", "unauthorized", errors.New("exit status 1"))
	if err == nil || !strings.Contains(err.Error(), "pull registry.example.net/app:1") || !strings.Contains(err.Error(), "docker login registry.example.net") {
		t.Errorf("imagePullError = %v, want the image and the login hint", err)
	}
}
