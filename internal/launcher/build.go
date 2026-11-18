package launcher

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"dev/internal/stack"
)

const (
	SourceDir         = "devbox/dev"
	keptBuildCount    = 3
	buildFolderPrefix = "build-"
	buildLockFileName = "build.lock"
	lockPollInterval  = 100 * time.Millisecond
	builtBinaryName   = "dev"
	defaultGoProxy    = "https://goproxy.example.com,direct"
	shortCommitLength = "12"
	newerGoHintFormat = "the checkout needs Go %s, the image ships Go %s: the devbox image must be updated"
)

var (
	sourceTreeFolderName   = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	newerGoRequiredMessage = regexp.MustCompile(`requires go >= ([^\s;)]+) \(running go ([^\s;)]+)`)
)

type Launcher struct {
	MainCheckoutDir string
	CacheDir        string
	OwnSourceTree   string
	BuildTimeout    time.Duration
	LockWaitTimeout time.Duration
	Notes           io.Writer
	GoBuild         func(ctx context.Context, sourceDir, outputPath, ldflags string, buildOutput io.Writer) error
}

type BuildError struct {
	FirstLine string
	Hint      string
}

func (buildError *BuildError) Error() string {
	return "build of dev failed: " + buildError.FirstLine
}

func RunGoBuild(ctx context.Context, sourceDir, outputPath, ldflags string, buildOutput io.Writer) error {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, goBinary, "build", "-ldflags", ldflags, "-o", outputPath, ".")
	command.Dir = sourceDir
	command.Env = append(command.Environ(), "GOTOOLCHAIN=local", "GOWORK=off")
	if os.Getenv("GOPROXY") == "" {
		command.Env = append(command.Env, "GOPROXY="+defaultGoProxy)
	}
	command.Stdout = buildOutput
	command.Stderr = buildOutput
	return command.Run()
}

func (launcher Launcher) SourceTreeOfCheckout() (sourceTree, shortHead string, err error) {
	sourceTreeOutput, err := stack.GitOutput(launcher.MainCheckoutDir, "rev-parse", "HEAD:"+SourceDir)
	if err != nil {
		return "", "", fmt.Errorf("read the commit of %s in %s: %w", SourceDir, launcher.MainCheckoutDir, err)
	}
	shortHeadOutput, err := stack.GitOutput(launcher.MainCheckoutDir, "rev-parse", "--short="+shortCommitLength, "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("read the commit of %s: %w", launcher.MainCheckoutDir, err)
	}
	return strings.TrimSpace(sourceTreeOutput), strings.TrimSpace(shortHeadOutput), nil
}

func (launcher Launcher) BinaryPathFor(sourceTree string) string {
	return filepath.Join(launcher.CacheDir, sourceTree, builtBinaryName)
}

func (launcher Launcher) Build(ctx context.Context, sourceTree, shortHead string, buildOutput io.Writer) (binaryPath string, err error) {
	return launcher.build(ctx, sourceTree, shortHead, buildOutput, false)
}

// The binary takes the place of the one cached for the committed tree, so plain dev runs it until the tree changes in git.
func (launcher Launcher) BuildLocal(ctx context.Context, sourceTree, shortHead string, buildOutput io.Writer) (binaryPath string, err error) {
	return launcher.build(ctx, sourceTree, shortHead, buildOutput, true)
}

func (launcher Launcher) build(ctx context.Context, sourceTree, shortHead string, buildOutput io.Writer, hasLocalChanges bool) (binaryPath string, err error) {
	binaryPath = launcher.BinaryPathFor(sourceTree)
	if _, err := os.Stat(binaryPath); err == nil {
		return binaryPath, nil
	}
	if err := os.MkdirAll(launcher.CacheDir, 0o755); err != nil {
		return "", err
	}
	unlock, err := launcher.lockBuilds(ctx)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if _, err := os.Stat(binaryPath); err == nil {
		return binaryPath, nil
	}
	if err := removeLeftoverBuildFolders(launcher.CacheDir); err != nil {
		return "", err
	}
	if _, err := fmt.Fprintf(launcher.Notes, "building dev from %s (%s)\n", launcher.MainCheckoutDir, describeSource(shortHead, hasLocalChanges)); err != nil {
		return "", err
	}
	buildDir, err := os.MkdirTemp(launcher.CacheDir, buildFolderPrefix)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(buildDir)) }()
	sourceDir := filepath.Join(buildDir, "src")
	if hasLocalChanges {
		err = launcher.exportWorkingTree(sourceDir)
	} else {
		err = launcher.exportSourceTree(sourceTree, sourceDir)
	}
	if err != nil {
		return "", err
	}
	newBinaryPath := filepath.Join(buildDir, builtBinaryName)
	if err := launcher.runGoBuildWithTimeLimit(ctx, sourceDir, newBinaryPath, ldflagsFor(sourceTree, shortHead, hasLocalChanges), buildOutput); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(newBinaryPath, binaryPath); err != nil {
		return "", err
	}
	return binaryPath, removeOldBuilds(launcher.CacheDir)
}

func describeSource(shortHead string, hasLocalChanges bool) string {
	if hasLocalChanges {
		return "commit " + shortHead + " with local changes"
	}
	return "commit " + shortHead
}

func ldflagsFor(sourceTree, shortHead string, hasLocalChanges bool) string {
	return fmt.Sprintf("-X 'main.stampedVersion=%s (checkout)' -X main.stampedSourceTree=%s", describeSource(shortHead, hasLocalChanges), sourceTree)
}

func (launcher Launcher) runGoBuildWithTimeLimit(ctx context.Context, sourceDir, outputPath, ldflags string, buildOutput io.Writer) error {
	buildContext := ctx
	if launcher.BuildTimeout > 0 {
		var cancel context.CancelFunc
		buildContext, cancel = context.WithTimeout(ctx, launcher.BuildTimeout)
		defer cancel()
	}
	var capturedOutput bytes.Buffer
	err := launcher.GoBuild(buildContext, sourceDir, outputPath, ldflags, io.MultiWriter(&capturedOutput, buildOutput))
	if err == nil {
		return nil
	}
	if errors.Is(buildContext.Err(), context.DeadlineExceeded) {
		return &BuildError{FirstLine: fmt.Sprintf("no result after %s", launcher.BuildTimeout), Hint: ""}
	}
	return &BuildError{FirstLine: firstErrorLine(capturedOutput.String(), err), Hint: hintFor(capturedOutput.String())}
}

func firstErrorLine(buildOutput string, err error) string {
	for line := range strings.Lines(buildOutput) {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "# ") {
			return line
		}
	}
	return err.Error()
}

func hintFor(buildOutput string) string {
	match := newerGoRequiredMessage.FindStringSubmatch(buildOutput)
	if match == nil {
		return ""
	}
	return fmt.Sprintf(newerGoHintFormat, match[1], match[2])
}

func (launcher Launcher) exportSourceTree(sourceTree, destinationDir string) error {
	archive, err := stack.GitOutput(launcher.MainCheckoutDir, "archive", "--format=tar", sourceTree)
	if err != nil {
		return fmt.Errorf("export %s of %s: %w", SourceDir, launcher.MainCheckoutDir, err)
	}
	archiveReader := tar.NewReader(strings.NewReader(archive))
	for {
		header, err := archiveReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := extractArchiveEntry(archiveReader, header, destinationDir); err != nil {
			return err
		}
	}
}

func (launcher Launcher) exportWorkingTree(destinationDir string) error {
	listing, err := stack.GitOutput(launcher.MainCheckoutDir, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", SourceDir)
	if err != nil {
		return fmt.Errorf("list %s in %s: %w", SourceDir, launcher.MainCheckoutDir, err)
	}
	for _, name := range strings.Split(listing, "\x00") {
		if name == "" {
			continue
		}
		sourcePath := filepath.Join(launcher.MainCheckoutDir, name)
		info, err := os.Lstat(sourcePath)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s of %s is not a file", name, SourceDir)
		}
		relativePath, err := filepath.Rel(SourceDir, name)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		destinationPath := filepath.Join(destinationDir, relativePath)
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(destinationPath, content, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func extractArchiveEntry(archiveReader io.Reader, header *tar.Header, destinationDir string) error {
	if header.Typeflag == tar.TypeXGlobalHeader {
		return nil
	}
	if !filepath.IsLocal(header.Name) {
		return fmt.Errorf("the archive of %s holds a path outside of it: %s", SourceDir, header.Name)
	}
	destinationPath := filepath.Join(destinationDir, header.Name)
	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(destinationPath, 0o755)
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
			return err
		}
		content, err := io.ReadAll(archiveReader)
		if err != nil {
			return err
		}
		return os.WriteFile(destinationPath, content, header.FileInfo().Mode().Perm())
	default:
		return fmt.Errorf("the archive of %s holds %s, which is not a file or a folder", SourceDir, header.Name)
	}
}

func (launcher Launcher) lockBuilds(ctx context.Context) (unlock func() error, err error) {
	lockFile, err := os.OpenFile(filepath.Join(launcher.CacheDir, buildLockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	waitContext := ctx
	if launcher.LockWaitTimeout > 0 {
		var cancel context.CancelFunc
		waitContext, cancel = context.WithTimeout(ctx, launcher.LockWaitTimeout)
		defer cancel()
	}
	for hasWaited := false; ; hasWaited = true {
		err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return lockFile.Close, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.Join(err, lockFile.Close())
		}
		if !hasWaited {
			if _, err := fmt.Fprintln(launcher.Notes, "waiting for another build of dev"); err != nil {
				return nil, errors.Join(err, lockFile.Close())
			}
		}
		select {
		case <-waitContext.Done():
			return nil, errors.Join(fmt.Errorf("wait for another build of dev: %w", waitContext.Err()), lockFile.Close())
		case <-time.After(lockPollInterval):
		}
	}
}

func removeLeftoverBuildFolders(cacheDir string) error {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), buildFolderPrefix) {
			if err := os.RemoveAll(filepath.Join(cacheDir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeOldBuilds(cacheDir string) error {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return err
	}
	type build struct {
		folder       string
		modification time.Time
	}
	var builds []build
	for _, entry := range entries {
		if !entry.IsDir() || !sourceTreeFolderName.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		builds = append(builds, build{folder: filepath.Join(cacheDir, entry.Name()), modification: info.ModTime()})
	}
	slices.SortFunc(builds, func(first, second build) int { return second.modification.Compare(first.modification) })
	for _, oldBuild := range builds[min(len(builds), keptBuildCount):] {
		if err := os.RemoveAll(oldBuild.folder); err != nil {
			return err
		}
	}
	return nil
}
