// Package shellenv reads the environment that a login shell gives, which is the base of the environment of every app.
package shellenv

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"
)

const (
	readTimeout = 15 * time.Second
	// A background job of a profile file keeps the output open. The environment is complete when the shell exits.
	outputWaitDelay = time.Second
	// A shell file can print a banner. The environment starts after this line.
	startMarker   = "__ilot_environment_starts_here__"
	fallbackShell = "/bin/sh"
	// The shell files of the system build the real PATH from this one.
	seedPath = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
)

// The variables of the session that no shell file can give again. Nothing else of the caller reaches the shell.
var sessionVariables = []string{"LANG", "LC_ALL", "TMPDIR", "SSH_AUTH_SOCK"}

// The variables that describe the shell process itself. A stale PWD would give an app the wrong folder.
var shellProcessVariables = []string{"PWD", "OLDPWD", "SHLVL", "_"}

type Shell struct {
	Program string
	// The flags that make the shell read its files, before the -c flag that ilot adds.
	Args []string
}

// OfUser is the shell of the user, with the flags of the global file. The default flag, -l, makes a login shell,
// which reads the profile files, and not the files of an interactive shell.
func OfUser(args []string) Shell {
	return Shell{Program: cmp.Or(os.Getenv("SHELL"), fallbackShell), Args: args}
}

// Environment starts the shell with a few seed variables, so that a value exported in the terminal of the caller never reaches an app.
func (shell Shell) Environment(ctx context.Context) (map[string]string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	seeds, err := seedEnvironment(homeDir, shell.Program)
	if err != nil {
		return nil, err
	}
	return shell.read(ctx, seeds, homeDir, readTimeout)
}

func (shell Shell) read(ctx context.Context, seeds []string, homeDir string, timeout time.Duration) (map[string]string, error) {
	errTimeout := fmt.Errorf("the shell did not exit in %s: a profile file waits for input or runs a long command", timeout)
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, errTimeout)
	defer cancel()
	arguments := append(append([]string{}, shell.Args...), "-c", "echo "+startMarker+"; env -0")
	command := exec.CommandContext(ctx, shell.Program, arguments...)
	command.Env = seeds
	command.Dir = homeDir
	command.WaitDelay = outputWaitDelay
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if cause := context.Cause(ctx); errors.Is(cause, errTimeout) {
		err = cause
	}
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		return nil, fmt.Errorf("read the environment of %s %s: %w: %s", shell.Program, strings.Join(shell.Args, " "), err, strings.TrimSpace(stderr.String()))
	}
	environment, err := parse(output)
	if err != nil {
		return nil, fmt.Errorf("read the environment of %s %s: %w", shell.Program, strings.Join(shell.Args, " "), err)
	}
	return environment, nil
}

func seedEnvironment(homeDir, shellProgram string) ([]string, error) {
	currentUser, err := user.Current()
	if err != nil {
		return nil, err
	}
	seeds := []string{"HOME=" + homeDir, "USER=" + currentUser.Username, "LOGNAME=" + currentUser.Username, "SHELL=" + shellProgram, "PATH=" + seedPath}
	for _, name := range sessionVariables {
		if value, isSet := os.LookupEnv(name); isSet {
			seeds = append(seeds, name+"="+value)
		}
	}
	return seeds, nil
}

func parse(output []byte) (map[string]string, error) {
	_, afterMarker, hasMarker := bytes.Cut(output, []byte(startMarker+"\n"))
	if !hasMarker {
		return nil, fmt.Errorf("the shell did not run the command of ilot: its output is %q", truncate(output))
	}
	environment := map[string]string{}
	for pair := range bytes.SplitSeq(afterMarker, []byte{0}) {
		name, value, hasValue := strings.Cut(string(pair), "=")
		if hasValue && name != "" {
			environment[name] = value
		}
	}
	for _, name := range shellProcessVariables {
		delete(environment, name)
	}
	return environment, nil
}

func truncate(output []byte) string {
	const longest = 200
	if len(output) > longest {
		return string(output[:longest]) + "..."
	}
	return string(output)
}
