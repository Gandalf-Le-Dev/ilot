package workspace

import (
	"os/exec"
	"slices"
	"strings"
	"sync"
)

// Git runs git and keeps each answer: one ilot command asks the same question for several stacks, and no ilot command changes a repo.
type Git struct {
	// Run starts one git process. A test replaces it to count the processes.
	Run func(dir string, arguments ...string) (string, error)

	mutex   sync.Mutex
	answers map[string]gitAnswer
}

type gitAnswer struct {
	output string
	err    error
}

func NewGit() *Git {
	return &Git{Run: runGit}
}

func (git *Git) Output(dir string, arguments ...string) (string, error) {
	question := strings.Join(append([]string{dir}, arguments...), "\x00")
	git.mutex.Lock()
	defer git.mutex.Unlock()
	if answer, isKnown := git.answers[question]; isKnown {
		return answer.output, answer.err
	}
	output, err := git.Run(dir, arguments...)
	if git.answers == nil {
		git.answers = map[string]gitAnswer{}
	}
	git.answers[question] = gitAnswer{output: output, err: err}
	return output, err
}

var repositoryOverridingVariables = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_PREFIX"}

// A git hook exports GIT_DIR and its siblings, which would point every git call at the hook's repository.
func environmentWithoutRepositoryOverrides(environment []string) []string {
	return slices.DeleteFunc(environment, func(variable string) bool {
		name, _, _ := strings.Cut(variable, "=")
		return slices.Contains(repositoryOverridingVariables, name)
	})
}

func runGit(dir string, arguments ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", dir}, arguments...)...)
	command.Env = environmentWithoutRepositoryOverrides(command.Environ())
	output, err := command.Output()
	return string(output), err
}
