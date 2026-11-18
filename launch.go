package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"dev/internal/appconfig"
	"dev/internal/launcher"
)

const (
	launcherBuildTimeout = 2 * time.Minute
	launcherLockMargin   = 30 * time.Second
	rebuildCommandName   = "rebuild"
	launchWarningPrefix  = "warning: run the dev of the image: "
)

type rebuildCommand struct {
	Local bool `help:"Build the uncommitted files of devbox/dev too. Plain dev runs this build until devbox/dev changes in git."`
}

func (command *rebuildCommand) Run(ctx context.Context) error {
	checkoutLauncher, err := launcherForThisBinary()
	if err != nil {
		return err
	}
	binaryPath, err := rebuildCheckoutBinary(ctx, checkoutLauncher, os.Stderr, command.Local)
	var buildError *launcher.BuildError
	if errors.As(err, &buildError) && buildError.Hint != "" {
		return fmt.Errorf("%w\n%s", err, buildError.Hint)
	}
	if err != nil {
		return err
	}
	versionOutput, err := exec.CommandContext(ctx, binaryPath, "--version").Output()
	if err != nil {
		return fmt.Errorf("run %s --version: %w", binaryPath, err)
	}
	fmt.Printf("built %s: %s", binaryPath, versionOutput)
	return nil
}

func rebuildCheckoutBinary(ctx context.Context, checkoutLauncher launcher.Launcher, buildOutput io.Writer, hasLocalChanges bool) (binaryPath string, err error) {
	sourceTree, shortHead, err := checkoutLauncher.SourceTreeOfCheckout()
	if err != nil {
		return "", err
	}
	if err := os.RemoveAll(filepath.Dir(checkoutLauncher.BinaryPathFor(sourceTree))); err != nil {
		return "", err
	}
	checkoutLauncher.BuildTimeout = 0
	if hasLocalChanges {
		return checkoutLauncher.BuildLocal(ctx, sourceTree, shortHead, buildOutput)
	}
	return checkoutLauncher.Build(ctx, sourceTree, shortHead, buildOutput)
}

func runCheckoutBinaryInstead(ctx context.Context) {
	checkoutLauncher, err := launcherForThisBinary()
	if err != nil {
		return
	}
	binaryPath, warning := checkoutBinaryToRun(ctx, checkoutLauncher, os.Args)
	fmt.Fprint(os.Stderr, warning)
	if binaryPath == "" {
		return
	}
	err = syscall.Exec(binaryPath, os.Args, os.Environ())
	fmt.Fprintln(os.Stderr, launchWarningPrefix+err.Error())
}

func launcherForThisBinary() (launcher.Launcher, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return launcher.Launcher{}, err
	}
	return launcher.Launcher{
		MainCheckoutDir: platformCheckoutForLauncher(homeDir),
		CacheDir:        filepath.Join(cmp.Or(os.Getenv("XDG_CACHE_HOME"), filepath.Join(homeDir, ".cache")), "dev"),
		OwnSourceTree:   stampedSourceTree,
		BuildTimeout:    launcherBuildTimeout,
		LockWaitTimeout: launcherBuildTimeout + launcherLockMargin,
		Notes:           os.Stderr,
		GoBuild:         launcher.RunGoBuild,
	}, nil
}

func checkoutBinaryToRun(ctx context.Context, checkoutLauncher launcher.Launcher, arguments []string) (binaryPath string, warning string) {
	if checkoutLauncher.OwnSourceTree == "" || (len(arguments) > 1 && arguments[1] == rebuildCommandName) {
		return "", ""
	}
	sourceTree, shortHead, err := checkoutLauncher.SourceTreeOfCheckout()
	if err != nil {
		return "", launchWarning(err)
	}
	if sourceTree == checkoutLauncher.OwnSourceTree {
		return "", ""
	}
	binaryPath, err = checkoutLauncher.Build(ctx, sourceTree, shortHead, io.Discard)
	if err != nil {
		return "", launchWarning(err)
	}
	return binaryPath, ""
}

func launchWarning(err error) string {
	warning := launchWarningPrefix + err.Error() + "\n"
	var buildError *launcher.BuildError
	if !errors.As(err, &buildError) {
		return warning
	}
	if buildError.Hint != "" {
		warning += buildError.Hint + "\n"
	}
	return warning + "run \"dev rebuild\" to see the full build output and try again\n"
}

// Not personal.Load: its strict decoding would let a field added by a newer dev lock out the build that knows it.
func platformCheckoutForLauncher(homeDir string) string {
	defaultPath := expandHome(defaultPlatformPath, homeDir)
	content, err := os.ReadFile(filepath.Join(homeDir, personalSettingsPath))
	if err != nil {
		return defaultPath
	}
	var settings struct {
		Repos map[string]appconfig.RepoConfig `yaml:"repos"`
	}
	if err := yaml.Unmarshal(content, &settings); err != nil {
		return defaultPath
	}
	return expandHome(cmp.Or(settings.Repos[appconfig.DefaultRepo].Path, defaultPlatformPath), homeDir)
}
