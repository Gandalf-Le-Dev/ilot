package workspace

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	MasterStackName = "master"
	SharedStackName = "devbox"
)

const dnsLabelMaxLength = 63

var (
	runsOfCharactersOutsideDNSLabels = regexp.MustCompile(`[^a-z0-9]+`)
	ticketKeyPattern                 = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])((?:proj|ops|task)-\d+)`)
)

// StackName gives the stack that a worktree belongs to.
func StackName(worktree Worktree) (string, error) {
	if worktree.IsMain {
		return MasterStackName, nil
	}
	for _, candidate := range []string{worktree.Branch, filepath.Base(worktree.Dir)} {
		if ticketKey := ticketKeyPattern.FindStringSubmatch(candidate); ticketKey != nil {
			return strings.ToLower(ticketKey[1]), nil
		}
	}
	return nameFromFolder(worktree.Dir)
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
