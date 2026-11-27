package shellenv

import (
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestKeepTakesTheLoginShellVariablesAndDropsTheRest(t *testing.T) {
	kept := Keep([]string{
		"HOME=/home/user", "PATH=/usr/bin", "USER=acme", "LOGNAME=acme", "LANG=C.UTF-8",
		"LC_DEVBOX_LOGIN=jdoe", "DEVBOX_VERSION=1.2.3", "DEVHOST_PORT_7=20007",
		"GOPATH=/home/user/go", "GOPROXY=https://proxy", "GOPRIVATE=git.example.com", "GONOSUMDB=git.example.com", "CGO_ENABLED=0",
		"DEPLOY_ENV=DEV", "REGION=DEV1", "LOGKIT_INFRASTRUCTURE=preproduction", "TEMPORAL_ADDRESS=localhost:7233",
		"MISE_TRUSTED_CONFIG_PATHS=/home/user", "NVM_DIR=/home/user/.nvm",
		"SSH_AUTH_SOCK=/tmp/agent", "GPG_TTY=/dev/pts/0", "ACME_APPLICATION_KEY=key", "CLAUDECODE=1", "TERM_PROGRAM=tmux",
		"TERM=xterm", "DIRENV_DIR=/src", "GOFLAGS=-mod=vendor", "ILOT_PROXY_PORT=1", "no-equal-sign",
	})

	want := []string{
		"CGO_ENABLED", "DEPLOY_ENV", "DEVBOX_VERSION", "DEVHOST_PORT_7", "GONOSUMDB", "GOPATH", "GOPRIVATE",
		"GOPROXY", "HOME", "LANG", "LC_DEVBOX_LOGIN", "LOGKIT_INFRASTRUCTURE", "LOGNAME",
		"MISE_TRUSTED_CONFIG_PATHS", "NVM_DIR", "PATH", "REGION", "SSH_AUTH_SOCK", "TEMPORAL_ADDRESS", "USER",
	}
	if got := slices.Sorted(maps.Keys(kept)); !slices.Equal(got, want) {
		t.Errorf("Keep kept %v, want %v", got, want)
	}
	if kept["GOPROXY"] != "https://proxy" || kept["DEVHOST_PORT_7"] != "20007" {
		t.Errorf("Keep changed the values: %v", kept)
	}
}

func TestBaseComesFromACleanShellAndNotFromThisOne(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh is not on PATH")
	}
	t.Setenv("REGION", "set-by-a-envrc")
	t.Setenv("DEVHOST_PORT_7", "set-by-a-envrc")
	t.Setenv("PATH", "/a-envrc-bin:"+os.Getenv("PATH"))

	base, err := Base(t.Context())
	if err != nil {
		t.Fatalf("Base: %v", err)
	}

	for _, name := range []string{"HOME", "PATH", "USER", "LOGNAME"} {
		if base[name] == "" {
			t.Errorf("base lacks %s: %v", name, base)
		}
	}
	for _, name := range []string{"REGION", "DEVHOST_PORT_7", "PATH"} {
		if strings.Contains(base[name], "a-envrc") {
			t.Errorf("%s=%q comes from this shell, want the value of the shell files", name, base[name])
		}
	}
	if base["HOME"] != os.Getenv("HOME") {
		t.Errorf("HOME = %q, want %q", base["HOME"], os.Getenv("HOME"))
	}
}
