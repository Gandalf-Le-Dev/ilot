package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/launcher"
	"github.com/Gandalf-Le-Dev/ilot/internal/stack"
)

type fakeGoBuild struct {
	calls       atomic.Int32
	buildOutput string
	isFailing   bool
}

func (fake *fakeGoBuild) build(ctx context.Context, sourceDir, outputPath, ldflags string, buildOutput io.Writer) error {
	fake.calls.Add(1)
	if _, err := io.WriteString(buildOutput, fake.buildOutput); err != nil {
		return err
	}
	if fake.isFailing {
		return errors.New("exit status 1")
	}
	return os.WriteFile(outputPath, []byte("#!/bin/sh\n"), 0o755)
}

func launcherOnCheckout(t *testing.T, mainCheckoutDir, ownSourceTree string, goBuild *fakeGoBuild) launcher.Launcher {
	t.Helper()
	return launcher.Launcher{
		MainCheckoutDir: mainCheckoutDir,
		CacheDir:        filepath.Join(t.TempDir(), "cache"),
		OwnSourceTree:   ownSourceTree,
		BuildTimeout:    0,
		LockWaitTimeout: 0,
		Notes:           io.Discard,
		GoBuild:         goBuild.build,
	}
}

func checkoutSourceTree(t *testing.T, checkoutDir string) string {
	t.Helper()
	output, err := stack.GitOutput(checkoutDir, "rev-parse", "HEAD:"+launcher.SourceDir)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(output)
}

func platformCheckoutWithDevSource(t *testing.T) string {
	t.Helper()
	return gitRepoWithFiles(t, map[string]string{"devbox/ilot/main.go": "package main\n"})
}

func TestLauncherIsOffWithoutAStampedTree(t *testing.T) {
	goBuild := &fakeGoBuild{}
	checkoutLauncher := launcherOnCheckout(t, platformCheckoutWithDevSource(t), "", goBuild)
	binaryPath, warning := checkoutBinaryToRun(context.Background(), checkoutLauncher, []string{"ilot", "ls"})
	if binaryPath != "" || warning != "" || goBuild.calls.Load() != 0 {
		t.Errorf("got (%q, %q) after %d builds, want nothing", binaryPath, warning, goBuild.calls.Load())
	}
}

func TestRebuildSkipsTheLauncher(t *testing.T) {
	goBuild := &fakeGoBuild{}
	checkoutLauncher := launcherOnCheckout(t, platformCheckoutWithDevSource(t), "unknown", goBuild)
	binaryPath, warning := checkoutBinaryToRun(context.Background(), checkoutLauncher, []string{"ilot", "rebuild"})
	if binaryPath != "" || warning != "" || goBuild.calls.Load() != 0 {
		t.Errorf("got (%q, %q) after %d builds, want nothing", binaryPath, warning, goBuild.calls.Load())
	}
}

func TestSameTreeRunsItself(t *testing.T) {
	checkoutDir := platformCheckoutWithDevSource(t)
	goBuild := &fakeGoBuild{}
	checkoutLauncher := launcherOnCheckout(t, checkoutDir, checkoutSourceTree(t, checkoutDir), goBuild)
	binaryPath, warning := checkoutBinaryToRun(context.Background(), checkoutLauncher, []string{"ilot", "ls"})
	if binaryPath != "" || warning != "" || goBuild.calls.Load() != 0 {
		t.Errorf("got (%q, %q) after %d builds, want nothing", binaryPath, warning, goBuild.calls.Load())
	}
}

func TestNewTreeReturnsTheBuiltBinary(t *testing.T) {
	checkoutDir := platformCheckoutWithDevSource(t)
	checkoutLauncher := launcherOnCheckout(t, checkoutDir, "unknown", &fakeGoBuild{})
	binaryPath, warning := checkoutBinaryToRun(context.Background(), checkoutLauncher, []string{"ilot", "ls"})
	if want := filepath.Join(checkoutLauncher.CacheDir, checkoutSourceTree(t, checkoutDir), "ilot"); binaryPath != want || warning != "" {
		t.Errorf("got (%q, %q), want (%q, \"\")", binaryPath, warning, want)
	}
}

func TestMissingCheckoutWarnsAndRunsItself(t *testing.T) {
	checkoutLauncher := launcherOnCheckout(t, filepath.Join(t.TempDir(), "nowhere"), "unknown", &fakeGoBuild{})
	binaryPath, warning := checkoutBinaryToRun(context.Background(), checkoutLauncher, []string{"ilot", "ls"})
	if binaryPath != "" || !strings.HasPrefix(warning, "warning: run the ilot of the image: ") || strings.Contains(warning, "ilot rebuild") {
		t.Errorf("got (%q, %q), want no binary and a warning without the rebuild line", binaryPath, warning)
	}
}

func TestFailedBuildWarnsWithTheRebuildCommand(t *testing.T) {
	goBuild := &fakeGoBuild{buildOutput: "go: go.mod requires go >= 1.27 (running go 1.26.4; GOTOOLCHAIN=local)\n", isFailing: true}
	checkoutLauncher := launcherOnCheckout(t, platformCheckoutWithDevSource(t), "unknown", goBuild)
	binaryPath, warning := checkoutBinaryToRun(context.Background(), checkoutLauncher, []string{"ilot", "ls"})
	wantWarning := "warning: run the ilot of the image: build of ilot failed: go: go.mod requires go >= 1.27 (running go 1.26.4; GOTOOLCHAIN=local)\n" +
		"the checkout needs Go 1.27, the image ships Go 1.26.4: the devbox image must be updated\n" +
		"run \"ilot rebuild\" to see the full build output and try again\n"
	if binaryPath != "" || warning != wantWarning {
		t.Errorf("got (%q, %q), want no binary and\n%s", binaryPath, warning, wantWarning)
	}
}

func TestRebuildBuildsAgainWhenCached(t *testing.T) {
	goBuild := &fakeGoBuild{}
	checkoutLauncher := launcherOnCheckout(t, platformCheckoutWithDevSource(t), "unknown", goBuild)
	for range 2 {
		if _, err := rebuildCheckoutBinary(context.Background(), checkoutLauncher, io.Discard, false); err != nil {
			t.Fatal(err)
		}
	}
	if calls := goBuild.calls.Load(); calls != 2 {
		t.Errorf("go build ran %d times, want 2", calls)
	}
}

func TestPlatformCheckoutForLauncherIgnoresUnknownFields(t *testing.T) {
	homeDir := homeWithPersonalFile(t, "newfield: 1\nrepos:\n  platform: {path: ~/elsewhere}\n")
	if got, want := platformCheckoutForLauncher(homeDir), filepath.Join(homeDir, "elsewhere"); got != want {
		t.Errorf("platformCheckoutForLauncher = %q, want %q", got, want)
	}
}

func TestPlatformCheckoutForLauncherFallsBackOnBrokenYAML(t *testing.T) {
	homeDir := homeWithPersonalFile(t, "repos: [\n")
	if got, want := platformCheckoutForLauncher(homeDir), expandHome(defaultPlatformPath, homeDir); got != want {
		t.Errorf("platformCheckoutForLauncher = %q, want %q", got, want)
	}
}

func TestInterruptDuringTheBuildStopsTheCommand(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	workDir := t.TempDir()
	imageCopy := filepath.Join(workDir, "ilot-image")
	build := exec.Command("go", "build", "-ldflags", "-X 'main.stampedVersion=test (image)' -X main.stampedSourceTree=unknown", "-o", imageCopy, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	hangingGoDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(hangingGoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hangingGoDir, "go"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	homeDir := homeWithPersonalFile(t, "repos:\n  platform: {path: "+platformCheckoutWithDevSource(t)+"}\n")
	command := exec.Command(imageCopy, "--version")
	command.Env = []string{"HOME=" + homeDir, "XDG_CACHE_HOME=" + filepath.Join(workDir, "cache"), "PATH=" + hangingGoDir + ":/usr/bin:/bin"}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Error(err)
		}
	})
	stderrLines := bufio.NewScanner(stderr)
	if !stderrLines.Scan() || !strings.HasPrefix(stderrLines.Text(), "building ilot from") {
		t.Fatalf("first stderr line = %q, want the building line", stderrLines.Text())
	}
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	var restOfStderr strings.Builder
	for stderrLines.Scan() {
		restOfStderr.WriteString(stderrLines.Text() + "\n")
	}
	err = command.Wait()
	if err == nil || restOfStderr.Len() > 0 {
		t.Errorf("after Ctrl-C: exit %v, stderr %q, want the command stopped with no warning", err, restOfStderr.String())
	}
}
