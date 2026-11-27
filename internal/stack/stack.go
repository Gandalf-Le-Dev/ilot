package stack

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/realpath"
)

const (
	MasterStackName = "master"
	SharedStackName = "devbox"
)

const dnsLabelMaxLength = 63

type Worktree struct {
	Name            string
	WorktreeDir     string
	MainCheckoutDir string
	Branch          string
}

var (
	runsOfCharactersOutsideDNSLabels = regexp.MustCompile(`[^a-z0-9]+`)
	ticketKeyPattern                 = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])((?:proj|ops|task)-\d+)`)
)

func NameForCheckout(checkoutDir, branch string, isMainCheckout bool) (string, error) {
	if isMainCheckout {
		return MasterStackName, nil
	}
	for _, candidate := range []string{branch, filepath.Base(checkoutDir)} {
		if ticketKey := ticketKeyPattern.FindStringSubmatch(candidate); ticketKey != nil {
			return strings.ToLower(ticketKey[1]), nil
		}
	}
	return nameFromFolder(checkoutDir)
}

func nameFromFolder(worktreeDir string) (string, error) {
	name := runsOfCharactersOutsideDNSLabels.ReplaceAllString(strings.ToLower(filepath.Base(worktreeDir)), "-")
	name = strings.Trim(name, "-")
	if len(name) > dnsLabelMaxLength {
		name = strings.TrimRight(name[:dnsLabelMaxLength], "-")
	}
	if name == "" {
		return "", fmt.Errorf("worktree directory %q gives no usable stack name: rename it with letters or digits", worktreeDir)
	}
	if name == MasterStackName || name == SharedStackName {
		return "", fmt.Errorf("worktree directory %q would take the reserved stack name %q: rename it", worktreeDir, name)
	}
	return name, nil
}

var repositoryOverridingVariables = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_PREFIX"}

// A git hook exports GIT_DIR and its siblings, which would point every git call at the hook's repository.
func environmentWithoutRepositoryOverrides(environment []string) []string {
	return slices.DeleteFunc(environment, func(variable string) bool {
		name, _, _ := strings.Cut(variable, "=")
		return slices.Contains(repositoryOverridingVariables, name)
	})
}

func GitOutput(dir string, arguments ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", dir}, arguments...)...)
	command.Env = environmentWithoutRepositoryOverrides(command.Environ())
	output, err := command.Output()
	return string(output), err
}

func Locate(dirInsideWorktree string) (Worktree, error) {
	output, err := GitOutput(dirInsideWorktree, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir", "--abbrev-ref", "HEAD")
	if err != nil {
		return Worktree{}, fmt.Errorf("%s is not inside a git checkout: %w", dirInsideWorktree, err)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 4 {
		return Worktree{}, fmt.Errorf("unexpected git rev-parse output %q", output)
	}
	worktreeDir, gitDir, commonGitDir, branch := realpath.Of(lines[0]), realpath.Of(lines[1]), realpath.Of(lines[2]), lines[3]
	isMainCheckout := gitDir == commonGitDir
	name, err := NameForCheckout(worktreeDir, branch, isMainCheckout)
	if err != nil {
		return Worktree{}, err
	}
	return Worktree{
		Name:            name,
		WorktreeDir:     worktreeDir,
		MainCheckoutDir: filepath.Dir(commonGitDir),
		Branch:          branch,
	}, nil
}
