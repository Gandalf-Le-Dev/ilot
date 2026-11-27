package shellenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeShell stands for a login shell: it records its flags, prints a banner as a profile file can, exports one variable, then runs the command.
func fakeShell(t *testing.T, profile string) (shell Shell, flagsFile string) {
	t.Helper()
	folder := t.TempDir()
	flagsFile = filepath.Join(folder, "flags")
	script := "#!/bin/sh\n" +
		"echo \"$1\" > '" + flagsFile + "'\n" +
		"echo 'Welcome to this machine'\n" +
		profile + "\n" +
		"shift\n" +
		"exec /bin/sh \"$@\"\n"
	program := filepath.Join(folder, "fake-shell")
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return Shell{Program: program, Args: []string{"-l"}}, flagsFile
}

func TestEnvironmentIsTheOneOfALoginShell(t *testing.T) {
	shell, flagsFile := fakeShell(t, "export PATH=\"/from/profile:$PATH\"\nexport EDITOR=vi")

	environment, err := shell.Environment(t.Context())
	if err != nil {
		t.Fatalf("Environment: %v", err)
	}

	if flags, err := os.ReadFile(flagsFile); err != nil || strings.TrimSpace(string(flags)) != "-l" {
		t.Errorf("the shell got the first flag %q, %v; want -l, which makes a login shell", flags, err)
	}
	if !strings.HasPrefix(environment["PATH"], "/from/profile:") || environment["EDITOR"] != "vi" {
		t.Errorf("PATH = %q, EDITOR = %q; want the values that the profile exports", environment["PATH"], environment["EDITOR"])
	}
	if environment["HOME"] != os.Getenv("HOME") || environment["USER"] == "" || environment["SHELL"] != shell.Program {
		t.Errorf("HOME = %q, USER = %q, SHELL = %q; want the identity of the user", environment["HOME"], environment["USER"], environment["SHELL"])
	}
}

func TestEnvironmentTakesNothingFromTheTerminalOfTheCaller(t *testing.T) {
	t.Setenv("API_TOKEN", "exported-in-a-terminal")
	t.Setenv("PATH", "/a-virtualenv/bin:"+os.Getenv("PATH"))
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	shell, _ := fakeShell(t, "")

	environment, err := shell.Environment(t.Context())
	if err != nil {
		t.Fatalf("Environment: %v", err)
	}

	if _, isSet := environment["API_TOKEN"]; isSet || strings.Contains(environment["PATH"], "a-virtualenv") {
		t.Errorf("API_TOKEN = %q, PATH = %q; want nothing of the caller", environment["API_TOKEN"], environment["PATH"])
	}
	if environment["SSH_AUTH_SOCK"] != "/tmp/agent.sock" {
		t.Errorf("SSH_AUTH_SOCK = %q, want the agent of the session, which no shell file can give", environment["SSH_AUTH_SOCK"])
	}
}

func TestEnvironmentLeavesOutTheVariablesOfTheShellProcess(t *testing.T) {
	shell, _ := fakeShell(t, "export MULTILINE='first\nsecond'")

	environment, err := shell.Environment(t.Context())
	if err != nil {
		t.Fatalf("Environment: %v", err)
	}

	for _, name := range []string{"PWD", "OLDPWD", "SHLVL", "_"} {
		if value, isSet := environment[name]; isSet {
			t.Errorf("%s = %q, want it left out: an app has its own folder", name, value)
		}
	}
	if environment["MULTILINE"] != "first\nsecond" {
		t.Errorf("MULTILINE = %q, want both lines", environment["MULTILINE"])
	}
}

func TestEnvironmentReportsAShellThatDoesNotRunTheCommand(t *testing.T) {
	folder := t.TempDir()
	program := filepath.Join(folder, "broken-shell")
	if err := os.WriteFile(program, []byte("#!/bin/sh\necho 'not a shell'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Shell{Program: program, Args: []string{"-l"}}.Environment(t.Context())

	if err == nil || !strings.Contains(err.Error(), program) || !strings.Contains(err.Error(), "not a shell") {
		t.Errorf("Environment = %v, want an error with the shell and its output", err)
	}
}

func TestOfUserUsesTheShellOfTheUserWithTheGivenFlags(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/fish")
	if shell := OfUser([]string{"-l", "-i"}); shell.Program != "/usr/bin/fish" || len(shell.Args) != 2 || shell.Args[1] != "-i" {
		t.Errorf("OfUser = %+v, want /usr/bin/fish -l -i", shell)
	}
	t.Setenv("SHELL", "")
	if shell := OfUser([]string{"-l"}); shell.Program != "/bin/sh" {
		t.Errorf("OfUser with no SHELL = %+v, want /bin/sh", shell)
	}
}
