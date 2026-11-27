package workspace

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// The stack of the main checkout of every repo, on any branch.
const BaseStackName = "base"

const dnsLabelMaxLength = 63

var (
	runsOfCharactersOutsideDNSLabels = regexp.MustCompile(`[^a-z0-9]+`)
	ticketKeyPattern                 = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])((?:proj|ops|task)-\d+)`)
)

// StackName gives the stack that a worktree belongs to.
func StackName(worktree Worktree) (string, error) {
	if worktree.IsMain {
		return BaseStackName, nil
	}
	for _, candidate := range []string{worktree.Branch, filepath.Base(worktree.Dir)} {
		if ticketKey := ticketKeyPattern.FindStringSubmatch(candidate); ticketKey != nil {
			return strings.ToLower(ticketKey[1]), nil
		}
	}
	return nameFromFolder(worktree.Dir)
}

func nameFromFolder(worktreeDir string) (string, error) {
	name := slug(filepath.Base(worktreeDir))
	if name == "" {
		return "", fmt.Errorf("worktree directory %q gives no usable stack name: rename it with letters or digits", worktreeDir)
	}
	if name == BaseStackName {
		return "", fmt.Errorf("worktree directory %q would take the reserved stack name %q: rename it", worktreeDir, name)
	}
	return name, nil
}

// slug turns a text into a DNS label: lowercase letters, digits and dashes, 63 characters at most.
func slug(text string) string {
	label := strings.Trim(runsOfCharactersOutsideDNSLabels.ReplaceAllString(strings.ToLower(text), "-"), "-")
	if len(label) > dnsLabelMaxLength {
		label = strings.TrimRight(label[:dnsLabelMaxLength], "-")
	}
	return label
}
