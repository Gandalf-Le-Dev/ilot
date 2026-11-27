package personal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOfAMissingFileGivesNoSettings(t *testing.T) {
	settings, err := Load(filepath.Join(t.TempDir(), "env.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(settings.Env) != 0 || len(settings.Apps) != 0 || len(settings.Repos) != 0 {
		t.Errorf("settings = %+v", settings)
	}
}

func TestLoadReadsEnvAppsAndRepos(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "env.yaml")
	content := "env:\n  LOG_LEVEL: jdoe\napps:\n  portal-test-runner:\n    PROJECT_ID: abc\nrepos:\n  portal: {path: ~/acme-portal}\n"
	if err := os.WriteFile(settingsPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := Load(settingsPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings.Env["LOG_LEVEL"] != "jdoe" || settings.Apps["portal-test-runner"]["PROJECT_ID"] != "abc" || settings.Repos["portal"].Path != "~/acme-portal" {
		t.Errorf("settings = %+v", settings)
	}
}

func TestLoadRefusesAnUnknownKey(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "env.yaml")
	if err := os.WriteFile(settingsPath, []byte("enviroment:\n  A: b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(settingsPath)
	if err == nil || !strings.Contains(err.Error(), settingsPath) {
		t.Fatalf("err = %v, want an error naming %s", err, settingsPath)
	}
}
