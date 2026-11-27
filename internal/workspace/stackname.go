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

var runsOfCharactersOutsideDNSLabels = regexp.MustCompile(`[^a-z0-9]+`)

// DefaultStackName gives the stack that a worktree has before its binding: base for a main checkout, and else a name from the branch.
// The branch is the natural key across repos: one line of work usually gets the same branch name in each repo.
// With a detached HEAD, the folder gives the name.
func (workspace Workspace) DefaultStackName(worktree Worktree) (string, error) {
	if worktree.IsMain {
		return BaseStackName, nil
	}
	source, sourceKind := worktree.Branch, "branch"
	if worktree.Branch == "" {
		source, sourceKind = filepath.Base(worktree.Dir), "folder"
	} else if workspace.StackPattern != nil {
		if match := workspace.StackPattern.FindString(worktree.Branch); match != "" {
			source = match
		}
	}
	name := slug(source)
	if name == "" {
		return "", fmt.Errorf("the %s %q of the worktree %s gives no stack name: a name needs a letter or a digit. Give the worktree a stack with ilot stack bind <name>", sourceKind, source, worktree.Dir)
	}
	if name == BaseStackName {
		return "", fmt.Errorf("the %s %q of the worktree %s gives the stack name %s, which is the stack of the main checkouts: give the worktree another stack with ilot stack bind <name>", sourceKind, source, worktree.Dir, BaseStackName)
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
