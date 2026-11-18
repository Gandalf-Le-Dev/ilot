package shellenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"strings"
	"time"
)

// The variables that devbox/config/shell/environment exports, with the login identity: nothing else of a shell reaches the apps.
var keptNames = []string{
	"HOME", "PATH", "USER", "LOGNAME", "LANG",
	"LC_DEVBOX_LOGIN", "DEVBOX_VERSION",
	"GOPATH", "GOPROXY", "GOPRIVATE", "GONOSUMDB", "CGO_ENABLED",
	"DEPLOY_ENV", "REGION", "MISE_TRUSTED_CONFIG_PATHS", "NPM_CONFIG_PREFIX", "SSH_AUTH_SOCK",
}

var keptPrefixes = []string{"DEVHOST_", "KEYRING_", "LOGKIT_", "TEMPORAL_", "NVM_"}

const cleanShellTimeout = 15 * time.Second

// zshenv replaces this PATH by the one of the image, and the environment file prepends the tools to it.
const seedPath = "/bin:/usr/bin"

func IsKept(name string) bool {
	return slices.Contains(keptNames, name) || slices.ContainsFunc(keptPrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
}

func Keep(environ []string) map[string]string {
	kept := map[string]string{}
	for _, pair := range environ {
		name, value, hasValue := strings.Cut(pair, "=")
		if hasValue && IsKept(name) {
			kept[name] = value
		}
	}
	return kept
}

// A fresh zsh sources /etc/zsh/zshenv, where the image put devbox/config/shell/environment: the caller's shell plays no part.
func Base(ctx context.Context) (map[string]string, error) {
	seeds, err := loginSeeds()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, cleanShellTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "zsh", "-c", "env -0")
	command.Env = seeds
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("read the environment of a clean zsh: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return Keep(strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")), nil
}

func loginSeeds() ([]string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	currentUser, err := user.Current()
	if err != nil {
		return nil, err
	}
	seeds := []string{"HOME=" + homeDir, "USER=" + currentUser.Username, "LOGNAME=" + currentUser.Username, "PATH=" + seedPath}
	if lang := os.Getenv("LANG"); lang != "" {
		seeds = append(seeds, "LANG="+lang)
	}
	return seeds, nil
}
