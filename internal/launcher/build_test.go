package launcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const tinyDevMain = `package main

import "fmt"

var stampedVersion string
var stampedSourceTree string

func main() {
	fmt.Println(stampedVersion)
	fmt.Println(stampedSourceTree)
}
`

func runGit(t *testing.T, repoDir string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repoDir}, arguments...)...)
	environmentWithoutGit := slices.DeleteFunc(command.Environ(), func(variable string) bool { return strings.HasPrefix(variable, "GIT_") })
	command.Env = append(environmentWithoutGit, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.net", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.net")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func checkoutWithDevSource(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	devSourceDir := filepath.Join(repoDir, SourceDir)
	if err := os.MkdirAll(devSourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for fileName, content := range map[string]string{"go.mod": "module dev\n\ngo 1.26\n", "main.go": tinyDevMain} {
		if err := os.WriteFile(filepath.Join(devSourceDir, fileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repoDir, "init", "-q", "-b", "master")
	runGit(t, repoDir, "add", "-A")
	runGit(t, repoDir, "commit", "-q", "-m", "init")
	return repoDir
}

type countingGoBuild struct {
	calls atomic.Int32
	delay time.Duration
}

func (fake *countingGoBuild) build(ctx context.Context, sourceDir, outputPath, ldflags string, buildOutput io.Writer) error {
	fake.calls.Add(1)
	time.Sleep(fake.delay)
	return os.WriteFile(outputPath, []byte("#!/bin/sh\n"), 0o755)
}

func failingGoBuild(output string) func(context.Context, string, string, string, io.Writer) error {
	return func(ctx context.Context, sourceDir, outputPath, ldflags string, buildOutput io.Writer) error {
		if _, err := io.WriteString(buildOutput, output); err != nil {
			return err
		}
		return errors.New("exit status 1")
	}
}

func testLauncher(t *testing.T, mainCheckoutDir string, goBuild func(context.Context, string, string, string, io.Writer) error) (Launcher, *bytes.Buffer) {
	t.Helper()
	var notes bytes.Buffer
	return Launcher{
		MainCheckoutDir: mainCheckoutDir,
		CacheDir:        filepath.Join(t.TempDir(), "cache"),
		OwnSourceTree:   "",
		BuildTimeout:    0,
		LockWaitTimeout: 0,
		Notes:           &notes,
		GoBuild:         goBuild,
	}, &notes
}

func TestBuildMakesABinaryStampedWithItsTree(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	checkoutDir := checkoutWithDevSource(t)
	launcher, notes := testLauncher(t, checkoutDir, RunGoBuild)
	sourceTree, shortHead, err := launcher.SourceTreeOfCheckout()
	if err != nil {
		t.Fatal(err)
	}
	binaryPath, err := launcher.Build(context.Background(), sourceTree, shortHead, io.Discard)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if binaryPath != filepath.Join(launcher.CacheDir, sourceTree, "dev") {
		t.Errorf("binaryPath = %s", binaryPath)
	}
	output, err := exec.Command(binaryPath).Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := "commit " + shortHead + " (checkout)\n" + sourceTree + "\n"; string(output) != want {
		t.Errorf("binary prints %q, want %q", output, want)
	}
	if want := "building dev from " + checkoutDir + " (commit " + shortHead + ")\n"; notes.String() != want {
		t.Errorf("notes = %q, want %q", notes.String(), want)
	}
}

func TestBuildUsesOnlyCommittedFiles(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	checkoutDir := checkoutWithDevSource(t)
	if err := os.WriteFile(filepath.Join(checkoutDir, SourceDir, "main.go"), []byte("package main\n\nthis does not compile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher, _ := testLauncher(t, checkoutDir, RunGoBuild)
	sourceTree, shortHead, err := launcher.SourceTreeOfCheckout()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := launcher.Build(context.Background(), sourceTree, shortHead, io.Discard); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

func TestBuildLocalUsesUncommittedFilesAndSaysSo(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	checkoutDir := checkoutWithDevSource(t)
	localMain := strings.Replace(tinyDevMain, "fmt.Println(stampedVersion)", "fmt.Println(stampedVersion, extra)", 1)
	for fileName, content := range map[string]string{"main.go": localMain, "extra.go": "package main\n\nconst extra = \"local-only\"\n"} {
		if err := os.WriteFile(filepath.Join(checkoutDir, SourceDir, fileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	launcher, _ := testLauncher(t, checkoutDir, RunGoBuild)
	sourceTree, shortHead, err := launcher.SourceTreeOfCheckout()
	if err != nil {
		t.Fatal(err)
	}
	binaryPath, err := launcher.BuildLocal(context.Background(), sourceTree, shortHead, io.Discard)
	if err != nil {
		t.Fatalf("BuildLocal: %v", err)
	}
	if binaryPath != launcher.BinaryPathFor(sourceTree) {
		t.Errorf("BuildLocal path = %q, want the path cached for the committed tree", binaryPath)
	}
	output, err := exec.Command(binaryPath).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "commit " + shortHead + " with local changes (checkout) local-only"
	if firstLine, _, _ := strings.Cut(string(output), "\n"); firstLine != want {
		t.Errorf("binary printed %q, want %q", firstLine, want)
	}
}

func TestBuildReusesTheLocalBuildForTheSameTree(t *testing.T) {
	checkoutDir := checkoutWithDevSource(t)
	fake := &countingGoBuild{}
	launcher, _ := testLauncher(t, checkoutDir, fake.build)
	sourceTree, shortHead, err := launcher.SourceTreeOfCheckout()
	if err != nil {
		t.Fatal(err)
	}
	localPath, err := launcher.BuildLocal(context.Background(), sourceTree, shortHead, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	plainPath, err := launcher.Build(context.Background(), sourceTree, shortHead, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if plainPath != localPath || fake.calls.Load() != 1 {
		t.Errorf("Build = %q after %d go builds, want %q after 1", plainPath, fake.calls.Load(), localPath)
	}
}

func TestBuildReusesACachedBinary(t *testing.T) {
	checkoutDir := checkoutWithDevSource(t)
	fakeGoBuild := &countingGoBuild{}
	launcher, notes := testLauncher(t, checkoutDir, fakeGoBuild.build)
	sourceTree, shortHead, err := launcher.SourceTreeOfCheckout()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := launcher.Build(context.Background(), sourceTree, shortHead, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if calls := fakeGoBuild.calls.Load(); calls != 1 {
		t.Errorf("go build ran %d times, want 1", calls)
	}
	if count := strings.Count(notes.String(), "building dev from"); count != 1 {
		t.Errorf("notes has %d building lines, want 1: %q", count, notes.String())
	}
}

func TestConcurrentBuildsOfOneTreeBuildOnce(t *testing.T) {
	checkoutDir := checkoutWithDevSource(t)
	fakeGoBuild := &countingGoBuild{delay: 200 * time.Millisecond}
	launcher, _ := testLauncher(t, checkoutDir, fakeGoBuild.build)
	launcher.Notes = io.Discard
	sourceTree, shortHead, err := launcher.SourceTreeOfCheckout()
	if err != nil {
		t.Fatal(err)
	}
	binaryPaths := make([]string, 2)
	buildErrors := make([]error, 2)
	var waitGroup sync.WaitGroup
	for index := range 2 {
		waitGroup.Go(func() {
			binaryPaths[index], buildErrors[index] = launcher.Build(context.Background(), sourceTree, shortHead, io.Discard)
		})
	}
	waitGroup.Wait()
	if buildErrors[0] != nil || buildErrors[1] != nil {
		t.Fatalf("errors = %v", buildErrors)
	}
	if binaryPaths[0] != binaryPaths[1] {
		t.Errorf("paths = %v, want the same path twice", binaryPaths)
	}
	if calls := fakeGoBuild.calls.Load(); calls != 1 {
		t.Errorf("go build ran %d times, want 1", calls)
	}
}

func TestFailedBuildReturnsTheFirstErrorLine(t *testing.T) {
	checkoutDir := checkoutWithDevSource(t)
	launcher, _ := testLauncher(t, checkoutDir, failingGoBuild("# dev\n./main.go:3:1: syntax error\n"))
	sourceTree, shortHead, err := launcher.SourceTreeOfCheckout()
	if err != nil {
		t.Fatal(err)
	}
	var buildOutput bytes.Buffer
	_, err = launcher.Build(context.Background(), sourceTree, shortHead, &buildOutput)
	var buildError *BuildError
	if !errors.As(err, &buildError) {
		t.Fatalf("err = %v, want a *BuildError", err)
	}
	if want := "build of dev failed: ./main.go:3:1: syntax error"; err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
	if !strings.Contains(buildOutput.String(), "syntax error") {
		t.Errorf("build output = %q, want the full go build output", buildOutput.String())
	}
	if _, err := os.Stat(filepath.Join(launcher.CacheDir, sourceTree)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the cache holds a folder for a failed build: %v", err)
	}
}

func TestBuildOfANewerGoGivesAHint(t *testing.T) {
	checkoutDir := checkoutWithDevSource(t)
	launcher, _ := testLauncher(t, checkoutDir, failingGoBuild("go: go.mod requires go >= 1.27 (running go 1.26.4; GOTOOLCHAIN=local)\n"))
	sourceTree, shortHead, err := launcher.SourceTreeOfCheckout()
	if err != nil {
		t.Fatal(err)
	}
	_, err = launcher.Build(context.Background(), sourceTree, shortHead, io.Discard)
	var buildError *BuildError
	if !errors.As(err, &buildError) {
		t.Fatalf("err = %v, want a *BuildError", err)
	}
	if want := "the checkout needs Go 1.27, the image ships Go 1.26.4: the devbox image must be updated"; buildError.Hint != want {
		t.Errorf("hint = %q, want %q", buildError.Hint, want)
	}
}

func TestBuildStopsAtTheTimeLimit(t *testing.T) {
	checkoutDir := checkoutWithDevSource(t)
	waitForCancel := func(ctx context.Context, sourceDir, outputPath, ldflags string, buildOutput io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}
	launcher, _ := testLauncher(t, checkoutDir, waitForCancel)
	launcher.BuildTimeout = 100 * time.Millisecond
	sourceTree, shortHead, err := launcher.SourceTreeOfCheckout()
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	_, err = launcher.Build(context.Background(), sourceTree, shortHead, io.Discard)
	var buildError *BuildError
	if !errors.As(err, &buildError) || buildError.FirstLine != "no result after 100ms" {
		t.Fatalf("err = %v, want a *BuildError with no result after 100ms", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Errorf("Build returned after %s", elapsed)
	}
}

func TestBuildRemovesLeftoverTemporaryFolders(t *testing.T) {
	checkoutDir := checkoutWithDevSource(t)
	launcher, _ := testLauncher(t, checkoutDir, (&countingGoBuild{}).build)
	leftoverDir := filepath.Join(launcher.CacheDir, "build-old")
	if err := os.MkdirAll(leftoverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leftoverDir, "dev"), []byte("half"), 0o755); err != nil {
		t.Fatal(err)
	}
	sourceTree, shortHead, err := launcher.SourceTreeOfCheckout()
	if err != nil {
		t.Fatal(err)
	}
	binaryPath, err := launcher.Build(context.Background(), sourceTree, shortHead, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(binaryPath, "build-old") {
		t.Errorf("binaryPath = %s, a leftover folder", binaryPath)
	}
	if _, err := os.Stat(leftoverDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the leftover folder is still there: %v", err)
	}
}

func TestBuildKeepsTheThreeNewestBuilds(t *testing.T) {
	checkoutDir := checkoutWithDevSource(t)
	launcher, _ := testLauncher(t, checkoutDir, (&countingGoBuild{}).build)
	oldTrees := []string{
		strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40), strings.Repeat("4", 40), strings.Repeat("5", 40),
	}
	oldestModification := time.Now().Add(-time.Hour)
	for index, oldTree := range oldTrees {
		treeDir := filepath.Join(launcher.CacheDir, oldTree)
		if err := os.MkdirAll(treeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(treeDir, "dev"), []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		modification := oldestModification.Add(time.Duration(index) * time.Minute)
		if err := os.Chtimes(treeDir, modification, modification); err != nil {
			t.Fatal(err)
		}
	}
	sourceTree, shortHead, err := launcher.SourceTreeOfCheckout()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := launcher.Build(context.Background(), sourceTree, shortHead, io.Discard); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(launcher.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	var keptTrees []string
	for _, entry := range entries {
		if entry.IsDir() {
			keptTrees = append(keptTrees, entry.Name())
		}
	}
	slices.Sort(keptTrees)
	wantTrees := []string{oldTrees[3], oldTrees[4], sourceTree}
	slices.Sort(wantTrees)
	if !slices.Equal(keptTrees, wantTrees) {
		t.Errorf("kept %v, want %v", keptTrees, wantTrees)
	}
}

func holdBuildLock(t *testing.T, cacheDir string) (release func()) {
	t.Helper()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockFile, err := os.OpenFile(filepath.Join(cacheDir, buildLockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := lockFile.Close(); err != nil {
			t.Error(err)
		}
	}
}

func buildWithDeadline(t *testing.T, ctx context.Context, launcher Launcher, sourceTree, shortHead string) (string, error) {
	t.Helper()
	type buildResult struct {
		binaryPath string
		err        error
	}
	results := make(chan buildResult, 1)
	go func() {
		binaryPath, err := launcher.Build(ctx, sourceTree, shortHead, io.Discard)
		results <- buildResult{binaryPath: binaryPath, err: err}
	}()
	select {
	case result := <-results:
		return result.binaryPath, result.err
	case <-time.After(3 * time.Second):
		t.Fatal("Build is still waiting after 3s")
		return "", nil
	}
}

func TestCachedBinaryIsFoundWhileAnotherBuildHoldsTheLock(t *testing.T) {
	checkoutDir := checkoutWithDevSource(t)
	launcher, _ := testLauncher(t, checkoutDir, (&countingGoBuild{}).build)
	sourceTree, shortHead, err := launcher.SourceTreeOfCheckout()
	if err != nil {
		t.Fatal(err)
	}
	cachedBinary := launcher.BinaryPathFor(sourceTree)
	if err := os.MkdirAll(filepath.Dir(cachedBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachedBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	release := holdBuildLock(t, launcher.CacheDir)
	defer release()
	binaryPath, err := buildWithDeadline(t, context.Background(), launcher, sourceTree, shortHead)
	if err != nil || binaryPath != cachedBinary {
		t.Errorf("got (%q, %v), want (%q, nil)", binaryPath, err, cachedBinary)
	}
}

func TestWaitingForTheLockStopsAtItsLimit(t *testing.T) {
	checkoutDir := checkoutWithDevSource(t)
	launcher, notes := testLauncher(t, checkoutDir, (&countingGoBuild{}).build)
	launcher.LockWaitTimeout = 300 * time.Millisecond
	sourceTree, shortHead, err := launcher.SourceTreeOfCheckout()
	if err != nil {
		t.Fatal(err)
	}
	release := holdBuildLock(t, launcher.CacheDir)
	defer release()
	if _, err := buildWithDeadline(t, context.Background(), launcher, sourceTree, shortHead); err == nil {
		t.Error("Build succeeded while another build held the lock")
	}
	if want := "waiting for another build of dev\n"; notes.String() != want {
		t.Errorf("notes = %q, want %q", notes.String(), want)
	}
}

func TestWaitingForTheLockStopsOnCancel(t *testing.T) {
	checkoutDir := checkoutWithDevSource(t)
	launcher, _ := testLauncher(t, checkoutDir, (&countingGoBuild{}).build)
	sourceTree, shortHead, err := launcher.SourceTreeOfCheckout()
	if err != nil {
		t.Fatal(err)
	}
	release := holdBuildLock(t, launcher.CacheDir)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := buildWithDeadline(t, ctx, launcher, sourceTree, shortHead); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context error", err)
	}
}
