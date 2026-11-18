package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

const (
	testToken             = "tok-SECRET-0123456789"
	testPassphrase        = "pass-SECRET-phrase"
	testUser              = "jane.doe@corp.example"
	testSigningKeyAsEmail = "jane.doe@corp.example"
	testFingerprint       = "AAAA1111BBBB2222CCCC3333DDDD4444EEEE5555"
	testOtherFingerprint  = "9999888877776666555544443333222211110000"
	testHost              = "art.corp.example"
)

func TestWriteNpmrcKeepsOtherLinesAndLeavesOneTokenLine(t *testing.T) {
	npmrcPath := filepath.Join(t.TempDir(), ".npmrc")
	if err := os.WriteFile(npmrcPath, []byte("registry=https://art.example/artifactory/api/npm/core/\n//art.example/artifactory/api/npm/a/:_authToken=old\n//art.example:443/artifactory/api/npm/b/:_authToken=old\nalways-auth=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteNpmrc(npmrcPath, "art.example", "new-token"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(npmrcPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "registry=https://art.example/artifactory/api/npm/core/\nalways-auth=true\n//art.example/artifactory/api/npm/:_authToken=new-token\n"
	if string(content) != want {
		t.Errorf("npmrc = %q", content)
	}
	if info, _ := os.Stat(npmrcPath); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
}

func TestWriteNpmrcCreatesAMissingFileWithMode600(t *testing.T) {
	npmrcPath := filepath.Join(t.TempDir(), ".npmrc")
	if err := WriteNpmrc(npmrcPath, "art.example", "new-token"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(npmrcPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v", info, err)
	}
}

func TestEnsureLinesAddsOnlyTheMissingLines(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gpg.conf")
	if err := os.WriteFile(configPath, []byte("keyid-format long\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLines(configPath, []string{"keyid-format long", "pinentry-mode loopback"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "keyid-format long\npinentry-mode loopback\n" {
		t.Errorf("gpg.conf = %q", content)
	}
}

func TestEnsureLinesCompletesAFileWithoutFinalNewline(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gpg.conf")
	if err := os.WriteFile(configPath, []byte("keyid-format long"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLines(configPath, []string{"pinentry-mode loopback"}); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(configPath)
	if string(content) != "keyid-format long\npinentry-mode loopback\n" {
		t.Errorf("gpg.conf = %q", content)
	}
}

func TestRegistryHostOfImage(t *testing.T) {
	hostByImage := map[string]string{
		"registry.example/team/devbox:1.2": "registry.example",
		"registry.example:5000/app:latest": "registry.example:5000",
		"traefik:v3.6":                     "",
		"devhome:local":                    "",
		"library/ubuntu:24.04":             "",
		"localhost/app:ilot":               "",
		"localhost:5000/app:ilot":          "",
	}
	for image, want := range hostByImage {
		if got := RegistryHostOfImage(image); got != want {
			t.Errorf("RegistryHostOfImage(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestRegistriesToLogInto(t *testing.T) {
	homeDir := t.TempDir()
	writeFile(t, filepath.Join(homeDir, ".docker", "config.json"), `{"auths":{"reg-b.corp.example":{},"https://reg-d.corp.example:8443/v2/":{},"reg-a.corp.example":{},"ghcr.io":{},"https://index.docker.io/v1/":{},"localhost:5000":{},"10.0.0.1:5000":{},"evilcorp.example":{},"corp.example":{},"art.corp.example.evil.net":{}}}`)

	registries, err := RegistriesToLogInto(homeDir, "art.corp.example", []string{"reg-c.corp.example/x:1", "traefik:v3.6", "reg-a.corp.example/y:2", "devagents:local", "localhost/app:ilot"})
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"reg-a.corp.example", "reg-c.corp.example"}; !slices.Equal(registries.FromImages, want) {
		t.Errorf("FromImages = %v, want %v", registries.FromImages, want)
	}
	if want := []string{"https://reg-d.corp.example:8443/v2/", "reg-b.corp.example"}; !slices.Equal(registries.FromDockerAuthsOnly, want) {
		t.Errorf("FromDockerAuthsOnly = %v, want %v", registries.FromDockerAuthsOnly, want)
	}
}

func TestRegistriesToLogIntoSendsNothingToAnyAuthsKeyWhenTheArtifactoryHostHasNoParentDomain(t *testing.T) {
	homeDir := t.TempDir()
	writeFile(t, filepath.Join(homeDir, ".docker", "config.json"), `{"auths":{"reg.example":{},"10.0.0.1":{}}}`)

	registries, err := RegistriesToLogInto(homeDir, "artifactory", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(registries.FromDockerAuthsOnly) != 0 {
		t.Errorf("FromDockerAuthsOnly = %v", registries.FromDockerAuthsOnly)
	}
}

func TestComposeImagesListsTheImageOfEveryService(t *testing.T) {
	composePath := filepath.Join(t.TempDir(), "docker-compose.yml")
	writeFile(t, composePath, "services:\n  a:\n    image: reg.example/a:${TAG:-1}\n  b:\n    image: traefik:v3.6\n  c:\n    build: .\n")
	images, err := ComposeImages(composePath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(images, []string{"reg.example/a:${TAG:-1}", "traefik:v3.6"}) {
		t.Errorf("images = %v", images)
	}
}

func TestRunStopsWhenPassUsesAnotherKeyThanSigning(t *testing.T) {
	fixture := newFixture(t)
	fixture.passKeyFingerprint = testOtherFingerprint

	_, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal())

	if err == nil || !strings.Contains(err.Error(), "pass init SIGNINGKEYLONG") {
		t.Fatalf("err = %v, want the pass init command with the signing key", err)
	}
	if fixture.ran("pass insert") || fixture.ran("jf ") {
		t.Errorf("commands ran after the key mismatch: %v", fixture.calls)
	}
}

func TestRunComparesFingerprintsWhenPassHoldsAnEmailAndGitALongId(t *testing.T) {
	fixture := newFixture(t)
	fixture.passKeySpec = testSigningKeyAsEmail

	if _, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal()); err != nil {
		t.Fatal(err)
	}
}

func TestRunAsksForTheTokenOnceAndStoresItInPass(t *testing.T) {
	fixture := newFixture(t)

	if _, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal()); err != nil {
		t.Fatal(err)
	}

	if fixture.passEntries["devbox/artifactory-token"] != testToken {
		t.Errorf("pass token entry = %q", fixture.passEntries["devbox/artifactory-token"])
	}
	if fixture.passEntries["devbox/artifactory-user"] != testUser {
		t.Errorf("pass user entry = %q", fixture.passEntries["devbox/artifactory-user"])
	}
	if fixture.count("pass insert") != 2 {
		t.Errorf("pass insert ran %d times, want 2", fixture.count("pass insert"))
	}
	npmrc, _ := os.ReadFile(filepath.Join(fixture.homeDir, ".npmrc"))
	if !strings.Contains(string(npmrc), "_authToken="+testToken) {
		t.Errorf("npmrc does not use the stored token: %q", npmrc)
	}
	if fixture.stdinOf("jf config add") != testToken+"\n" {
		t.Errorf("jf stdin = %q", fixture.stdinOf("jf config add"))
	}
	if fixture.stdinOf("docker login reg-a.corp.example") != testToken+"\n" {
		t.Errorf("docker login stdin = %q", fixture.stdinOf("docker login reg-a.corp.example"))
	}
	if !fixture.ran("docker login reg-a.corp.example --username " + testUser + " --password-stdin") {
		t.Errorf("docker login command missing: %v", fixture.calls)
	}
}

func TestRunDoesNotAskAgainWhenPassHoldsTheEntries(t *testing.T) {
	fixture := newFixture(t)
	fixture.passEntries["devbox/artifactory-token"] = testToken
	fixture.passEntries["devbox/artifactory-user"] = testUser
	fixture.writeEntryFiles()
	fixture.terminalInput = testPassphrase + "\n"

	if _, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal()); err != nil {
		t.Fatal(err)
	}
	if fixture.ran("pass insert") {
		t.Errorf("pass insert ran: %v", fixture.calls)
	}
}

func TestRunNeverPrintsTheToken(t *testing.T) {
	fixture := newFixture(t)
	terminal := fixture.terminal()

	changes, err := Run(context.Background(), fixture.settings(), fixture.runner, terminal)
	if err != nil {
		t.Fatal(err)
	}

	secrets := []string{testToken, testPassphrase}
	for _, secret := range secrets {
		if strings.Contains(strings.Join(changes, "\n"), secret) {
			t.Errorf("changes contain a secret: %v", changes)
		}
		if strings.Contains(fixture.terminalOutput.String(), secret) {
			t.Errorf("terminal output contains a secret: %q", fixture.terminalOutput.String())
		}
		for _, call := range fixture.calls {
			if strings.Contains(call, secret) {
				t.Errorf("a command line contains a secret: %q", call)
			}
		}
	}
}

func TestRunErrorsNeverContainTheToken(t *testing.T) {
	fixture := newFixture(t)
	fixture.failCommand = "docker login"

	_, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal())

	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error contains the token: %v", err)
	}
}

func TestRunSecondRunChangesNoFile(t *testing.T) {
	fixture := newFixture(t)
	if _, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal()); err != nil {
		t.Fatal(err)
	}
	fixture.terminalInput = ""

	changes, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal())
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Errorf("second run changes = %v, want none", changes)
	}
}

func TestRunConfiguresGpgForLoopbackAndTheDockerCredentialStore(t *testing.T) {
	fixture := newFixture(t)

	if _, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal()); err != nil {
		t.Fatal(err)
	}

	gpgConf, _ := os.ReadFile(filepath.Join(fixture.homeDir, ".gnupg", "gpg.conf"))
	if string(gpgConf) != "pinentry-mode loopback\n" {
		t.Errorf("gpg.conf = %q", gpgConf)
	}
	agentConf, _ := os.ReadFile(filepath.Join(fixture.homeDir, ".gnupg", "gpg-agent.conf"))
	if string(agentConf) != "allow-loopback-pinentry\ndefault-cache-ttl 34560000\nmax-cache-ttl 34560000\n" {
		t.Errorf("gpg-agent.conf = %q", agentConf)
	}
	dockerConfig, _ := os.ReadFile(filepath.Join(fixture.homeDir, ".docker", "config.json"))
	if !strings.Contains(string(dockerConfig), `"credsStore": "pass"`) {
		t.Errorf("docker config = %s", dockerConfig)
	}
	if !fixture.ran("gpg-connect-agent reloadagent /bye") {
		t.Errorf("agent not reloaded: %v", fixture.calls)
	}
}

func TestRunUnlocksWithSshAddOnlyWhenTheAgentHoldsNoKey(t *testing.T) {
	fixture := newFixture(t)
	fixture.sshAgentHoldsKey = true
	if _, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal()); err != nil {
		t.Fatal(err)
	}
	if fixture.ran("ssh-add\n") {
		t.Errorf("ssh-add ran with a key loaded")
	}

	other := newFixture(t)
	if _, err := Run(context.Background(), other.settings(), other.runner, other.terminal()); err != nil {
		t.Fatal(err)
	}
	if !other.ran("ssh-add\n") {
		t.Errorf("ssh-add did not run: %v", other.calls)
	}
}

func TestRunNamesTheDevhostKeyAndTheSetupDocsSectionWhenSshAddFailsWithNoPrivateKey(t *testing.T) {
	fixture := newFixture(t)
	fixture.failCommand = "ssh-add\n"
	writeFile(t, filepath.Join(fixture.homeDir, ".ssh", "authorized_keys"), "ssh-ed25519 AAAA laptop\n")
	writeFile(t, filepath.Join(fixture.homeDir, ".ssh", "id_ed25519.pub"), "ssh-ed25519 AAAA devhost\n")

	_, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal())

	if err == nil || !strings.Contains(err.Error(), "~/.ssh/id_ed25519") || !strings.Contains(err.Error(), `"The devhost SSH key"`) {
		t.Errorf("err = %v, want one that names ~/.ssh/id_ed25519 and the setup docs section \"The devhost SSH key\"", err)
	}
}

func TestRunKeepsTheSshAddErrorWhenAPrivateKeyIsThere(t *testing.T) {
	fixture := newFixture(t)
	fixture.failCommand = "ssh-add\n"
	writeFile(t, filepath.Join(fixture.homeDir, ".ssh", "id_rsa"), "private key\n")

	_, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal())

	if err == nil || !strings.Contains(err.Error(), "exit status 1") || strings.Contains(err.Error(), "The devhost SSH key") {
		t.Errorf("err = %v, want the plain ssh-add error", err)
	}
}

func TestRunSignsWithLoopbackAndThePassphraseOnStdinOnlyWhenTheAgentHoldsNoPassphrase(t *testing.T) {
	fixture := newFixture(t)

	if _, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal()); err != nil {
		t.Fatal(err)
	}

	if fixture.stdinOf("gpg --batch --pinentry-mode loopback") != testPassphrase+"\n" {
		t.Errorf("loopback signature stdin = %q", fixture.stdinOf("gpg --batch --pinentry-mode loopback"))
	}
}

type fixture struct {
	t                  *testing.T
	homeDir            string
	passKeySpec        string
	passKeyFingerprint string
	passEntries        map[string]string
	sshAgentHoldsKey   bool
	signingUnlocked    bool
	decryptionUnlocked bool
	replaceToken       bool
	dockerConfig       string
	failCommand        string
	terminalInput      string
	terminalOutput     bytes.Buffer
	calls              []string
	stdins             map[string]string
	loggedInRegistries map[string]bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	homeDir := t.TempDir()
	writeFile(t, filepath.Join(homeDir, ".docker", "config.json"), `{"auths":{"reg-a.corp.example":{}},"other":"kept"}`)
	return &fixture{
		t:                  t,
		homeDir:            homeDir,
		passKeySpec:        "PASSKEYLONG",
		passKeyFingerprint: testFingerprint,
		passEntries:        map[string]string{},
		sshAgentHoldsKey:   false,
		signingUnlocked:    false,
		decryptionUnlocked: false,
		replaceToken:       false,
		dockerConfig:       "",
		failCommand:        "",
		terminalInput:      testPassphrase + "\n" + testToken + "\n" + testUser + "\n",
		terminalOutput:     bytes.Buffer{},
		calls:              nil,
		stdins:             map[string]string{},
		loggedInRegistries: map[string]bool{},
	}
}

func (fixture *fixture) settings() Settings {
	if fixture.dockerConfig != "" {
		writeFile(fixture.t, filepath.Join(fixture.homeDir, ".docker", "config.json"), fixture.dockerConfig)
	}
	writeFile(fixture.t, filepath.Join(fixture.homeDir, ".password-store", ".gpg-id"), fixture.passKeySpec+"\n")
	return Settings{
		HomeDir:         fixture.homeDir,
		ArtifactoryHost: testHost,
		ImageReferences: []string{"reg-a.corp.example/devbox:1", "traefik:v3.6", "devhome:local", "localhost/app:ilot"},
		ReplaceToken:    fixture.replaceToken,
	}
}

func (fixture *fixture) writeEntryFiles() {
	for entry := range fixture.passEntries {
		writeFile(fixture.t, filepath.Join(fixture.homeDir, ".password-store", entry+".gpg"), "encrypted")
	}
}

type fakeTerminal struct {
	io.Reader
	io.Writer
}

func (fixture *fixture) terminal() io.ReadWriter {
	return fakeTerminal{Reader: strings.NewReader(fixture.terminalInput), Writer: &fixture.terminalOutput}
}

func (fixture *fixture) ran(commandPrefix string) bool {
	return fixture.count(commandPrefix) > 0
}

func (fixture *fixture) count(commandPrefix string) int {
	matching := 0
	for _, call := range fixture.calls {
		if strings.HasPrefix(call, commandPrefix) {
			matching++
		}
	}
	return matching
}

func (fixture *fixture) stdinOf(commandPrefix string) string {
	for _, call := range fixture.calls {
		if strings.HasPrefix(call, commandPrefix) {
			return fixture.stdins[call]
		}
	}
	return ""
}

func (fixture *fixture) runner(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	commandLine := name + " " + strings.Join(args, " ")
	if len(args) == 0 {
		commandLine = name + "\n"
	}
	fixture.calls = append(fixture.calls, commandLine)
	if stdin != nil {
		stdinContent, err := io.ReadAll(stdin)
		if err != nil {
			return nil, err
		}
		fixture.stdins[commandLine] = string(stdinContent)
	}
	if fixture.failCommand != "" && strings.HasPrefix(commandLine, fixture.failCommand) {
		return nil, errors.New("exit status 1")
	}
	switch {
	case commandLine == "git config user.signingkey":
		return []byte("SIGNINGKEYLONG\n"), nil
	case strings.HasPrefix(commandLine, "gpg --list-keys --with-colons "):
		return fixture.listKeys(strings.TrimPrefix(commandLine, "gpg --list-keys --with-colons ")), nil
	case commandLine == "pass show devbox/artifactory-token", commandLine == "pass show devbox/artifactory-user":
		if !fixture.decryptionUnlocked {
			return nil, errors.New("decryption failed")
		}
		entry := strings.TrimPrefix(commandLine, "pass show ")
		value, isStored := fixture.passEntries[entry]
		if !isStored {
			return nil, errors.New("not in the password store")
		}
		return []byte(value + "\n"), nil
	case strings.HasPrefix(commandLine, "pass insert --multiline --force "):
		entry := strings.TrimPrefix(commandLine, "pass insert --multiline --force ")
		fixture.passEntries[entry] = strings.TrimSpace(fixture.stdins[commandLine])
		writeFile(fixture.t, filepath.Join(fixture.homeDir, ".password-store", entry+".gpg"), "encrypted")
		return nil, nil
	case strings.HasPrefix(commandLine, "jf config add"):
		writeFile(fixture.t, filepath.Join(fixture.homeDir, ".jfrog", "jfrog-cli.conf.v6"), `{"servers":[{"serverId":"`+testHost+`","accessToken":"`+strings.TrimSpace(fixture.stdins[commandLine])+`"}]}`)
		return nil, nil
	case commandLine == "docker-credential-pass list":
		listing := "{"
		for registry := range fixture.loggedInRegistries {
			listing += `"https://` + registry + `/":"` + testUser + `",`
		}
		return []byte(strings.TrimSuffix(listing, ",") + "}"), nil
	case strings.HasPrefix(commandLine, "docker login"):
		fixture.loggedInRegistries[args[1]] = true
		return nil, nil
	case commandLine == "gpg-connect-agent reloadagent /bye":
		return nil, nil
	case commandLine == "ssh-add -l":
		if fixture.sshAgentHoldsKey {
			return []byte("256 SHA256:x key\n"), nil
		}
		return nil, ExitError{Code: 1}
	case commandLine == "ssh-add\n":
		fixture.sshAgentHoldsKey = true
		return nil, nil
	case strings.HasPrefix(commandLine, "gpg --batch --yes --trust-model always --recipient "+testFingerprint):
		return nil, nil
	case strings.HasPrefix(commandLine, "gpg --batch --pinentry-mode error") && strings.Contains(commandLine, " --clearsign "):
		return fixture.unlockedOrFail(fixture.signingUnlocked)
	case strings.HasPrefix(commandLine, "gpg --batch --pinentry-mode error") && strings.Contains(commandLine, " --decrypt "):
		return fixture.unlockedOrFail(fixture.decryptionUnlocked)
	case strings.HasPrefix(commandLine, "gpg --batch --pinentry-mode loopback") && strings.Contains(commandLine, " --clearsign "):
		if fixture.stdins[commandLine] != testPassphrase+"\n" {
			return nil, ExitError{Code: 2}
		}
		fixture.signingUnlocked = true
		return []byte("signature"), nil
	case strings.HasPrefix(commandLine, "gpg --batch --pinentry-mode loopback") && strings.Contains(commandLine, " --decrypt "):
		if fixture.stdins[commandLine] != testPassphrase+"\n" {
			return nil, ExitError{Code: 2}
		}
		fixture.decryptionUnlocked = true
		return []byte("probe"), nil
	}
	fixture.t.Fatalf("unexpected command: %q", commandLine)
	return nil, nil
}

func (fixture *fixture) unlockedOrFail(isUnlocked bool) ([]byte, error) {
	if !isUnlocked {
		return nil, ExitError{Code: 2}
	}
	return []byte("ok"), nil
}

func (fixture *fixture) listKeys(spec string) []byte {
	fingerprint := testFingerprint
	switch spec {
	case "SIGNINGKEYLONG", testSigningKeyAsEmail:
	case "PASSKEYLONG":
		fingerprint = fixture.passKeyFingerprint
	default:
		fingerprint = fixture.passKeyFingerprint
	}
	return []byte("tru::1:1700000000:0:3:1:5\npub:u:255:22:LONGID:1700000000:::u:::scESC:::::ed25519:::0:\nfpr:::::::::" + fingerprint + ":\nuid:u::::1700000000::HASH::Jane <jane.doe@corp.example>::::::::::0:\nsub:u:255:18:SUBID:1700000000::::::e:::::cv25519::\nfpr:::::::::0000000000000000000000000000000000000000:\n")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunUnlocksTheDecryptionKeyWithTheSamePassphrase(t *testing.T) {
	fixture := newFixture(t)

	if _, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal()); err != nil {
		t.Fatal(err)
	}

	if !fixture.decryptionUnlocked || !fixture.signingUnlocked {
		t.Errorf("signing unlocked = %v, decryption unlocked = %v", fixture.signingUnlocked, fixture.decryptionUnlocked)
	}
	if fixture.stdinOf("gpg --batch --pinentry-mode loopback --passphrase-fd 0 --output - --decrypt") != testPassphrase+"\n" {
		t.Errorf("decryption stdin = %q", fixture.stdinOf("gpg --batch --pinentry-mode loopback --passphrase-fd 0 --output - --decrypt"))
	}
}

func TestRunDoesNotAskForTheTokenWhenAlreadyStoredAndNoNewTokenIsAsked(t *testing.T) {
	fixture := newFixture(t)
	fixture.passEntries["devbox/artifactory-token"] = "old-token"
	fixture.passEntries["devbox/artifactory-user"] = testUser
	fixture.writeEntryFiles()
	fixture.terminalInput = testPassphrase + "\n"

	if _, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal()); err != nil {
		t.Fatal(err)
	}
	if fixture.stdinOf("jf config add") != "old-token\n" {
		t.Errorf("jf stdin = %q", fixture.stdinOf("jf config add"))
	}
}

func TestRunWithNewTokenReplacesTheStoredToken(t *testing.T) {
	fixture := newFixture(t)
	fixture.passEntries["devbox/artifactory-token"] = "old-token"
	fixture.passEntries["devbox/artifactory-user"] = testUser
	fixture.writeEntryFiles()
	fixture.replaceToken = true
	fixture.terminalInput = testPassphrase + "\n" + testToken + "\n"

	changes, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal())
	if err != nil {
		t.Fatal(err)
	}

	if fixture.passEntries["devbox/artifactory-token"] != testToken {
		t.Errorf("pass token entry = %q", fixture.passEntries["devbox/artifactory-token"])
	}
	if fixture.stdinOf("docker login reg-a.corp.example") != testToken+"\n" {
		t.Errorf("docker login stdin = %q", fixture.stdinOf("docker login reg-a.corp.example"))
	}
	npmrc, _ := os.ReadFile(filepath.Join(fixture.homeDir, ".npmrc"))
	if !strings.Contains(string(npmrc), "_authToken="+testToken) {
		t.Errorf("npmrc = %q", npmrc)
	}
	if strings.Contains(strings.Join(changes, "\n"), testToken) {
		t.Errorf("changes contain the token: %v", changes)
	}
}

func TestWriteNpmrcRefusesASymlinkAndLeavesItAndItsTargetAlone(t *testing.T) {
	directory := t.TempDir()
	targetPath := filepath.Join(directory, "real-npmrc")
	writeFile(t, targetPath, "always-auth=true\n")
	linkPath := filepath.Join(directory, ".npmrc")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatal(err)
	}

	err := WriteNpmrc(linkPath, "art.example", "new-token")

	if err == nil || !strings.Contains(err.Error(), targetPath) || !strings.Contains(err.Error(), "by hand") {
		t.Fatalf("err = %v, want one naming the link target and the manual step", err)
	}
	if strings.Contains(err.Error(), "new-token") {
		t.Errorf("error contains the token: %v", err)
	}
	if linkTarget, readErr := os.Readlink(linkPath); readErr != nil || linkTarget != targetPath {
		t.Errorf("link = %q, %v", linkTarget, readErr)
	}
	if content, _ := os.ReadFile(targetPath); string(content) != "always-auth=true\n" {
		t.Errorf("target = %q", content)
	}
}

func TestWriteNpmrcReplacesTheFileAtomicallyThroughATempFileInTheSameFolder(t *testing.T) {
	directory := t.TempDir()
	npmrcPath := filepath.Join(directory, ".npmrc")
	writeFile(t, npmrcPath, "always-auth=true\n")
	inodeBefore := inodeOf(t, npmrcPath)

	if err := WriteNpmrc(npmrcPath, "art.example", "new-token"); err != nil {
		t.Fatal(err)
	}

	if inodeOf(t, npmrcPath) == inodeBefore {
		t.Errorf("the file was rewritten in place")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Errorf("folder = %v, %v; want only .npmrc", entries, err)
	}
}

func TestRunStopsAtTheNpmrcSymlinkWithoutPrintingTheToken(t *testing.T) {
	fixture := newFixture(t)
	targetPath := filepath.Join(fixture.homeDir, "dotfiles-npmrc")
	writeFile(t, targetPath, "")
	if err := os.Symlink(targetPath, filepath.Join(fixture.homeDir, ".npmrc")); err != nil {
		t.Fatal(err)
	}

	_, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal())

	if err == nil || !strings.Contains(err.Error(), targetPath) || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunLogsInToTheImageRegistriesAndNotToLocalhost(t *testing.T) {
	fixture := newFixture(t)

	if _, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal()); err != nil {
		t.Fatal(err)
	}

	if fixture.ran("docker login localhost") {
		t.Errorf("docker login ran for localhost: %v", fixture.calls)
	}
}

func TestRunSkipsAnAuthsRegistryWhoseLoginFailsAndGoesOn(t *testing.T) {
	fixture := newFixture(t)
	fixture.dockerConfig = `{"auths":{"reg-a.corp.example":{},"reg-y.corp.example":{},"reg-z.corp.example":{}}}`
	fixture.failCommand = "docker login reg-y.corp.example"

	changes, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal())
	if err != nil {
		t.Fatal(err)
	}

	if !fixture.ran("docker login reg-z.corp.example") {
		t.Errorf("the loop stopped at the failing registry: %v", fixture.calls)
	}
	if !slices.Contains(changes, "skipped docker login to reg-y.corp.example") {
		t.Errorf("changes = %v", changes)
	}
}

func TestRunStopsWhenTheLoginToAnImageRegistryFails(t *testing.T) {
	fixture := newFixture(t)
	fixture.failCommand = "docker login reg-a.corp.example"

	if _, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal()); err == nil {
		t.Fatal("want an error")
	}
}

func TestRunWarnsWhichAuthsHoldInlineCredentialsBeforeTheCredentialStoreHidesThem(t *testing.T) {
	fixture := newFixture(t)
	fixture.dockerConfig = `{"auths":{"reg-a.corp.example":{},"ghcr.io":{"auth":"SECRETBASE64"},"docker.io":{"auth":"OTHERSECRET"}}}`
	fixture.failCommand = "docker login ghcr.io"

	if _, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal()); err != nil {
		t.Fatal(err)
	}

	output := fixture.terminalOutput.String()
	if strings.Count(output, "warning") != 1 || !strings.Contains(output, "ghcr.io") || !strings.Contains(output, "docker.io") {
		t.Errorf("terminal output = %q", output)
	}
	if strings.Contains(output, "SECRETBASE64") || strings.Contains(output, "OTHERSECRET") {
		t.Errorf("terminal output contains an inline credential: %q", output)
	}
}

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

func TestRunNeverSendsTheTokenToAForeignAuthsRegistry(t *testing.T) {
	fixture := newFixture(t)
	fixture.dockerConfig = `{"auths":{"reg-a.corp.example":{},"localhost:5000":{},"https://index.docker.io/v1/":{},"ghcr.io":{},"evilcorp.example":{},"reg-z.corp.example":{}}}`

	changes, err := Run(context.Background(), fixture.settings(), fixture.runner, fixture.terminal())
	if err != nil {
		t.Fatal(err)
	}

	wantLogins := []string{
		"docker login reg-a.corp.example --username " + testUser + " --password-stdin",
		"docker login reg-z.corp.example --username " + testUser + " --password-stdin",
	}
	if got := fixture.loginCommands(); !slices.Equal(got, wantLogins) {
		t.Errorf("docker login commands = %v, want %v", got, wantLogins)
	}
	if skipped := slices.IndexFunc(changes, func(change string) bool { return strings.HasPrefix(change, "skipped") }); skipped >= 0 {
		t.Errorf("a foreign key is listed as skipped: %v", changes)
	}
}

func TestRegistriesToLogIntoSendsNothingWhenTheParentDomainIsATopLevelDomain(t *testing.T) {
	homeDir := t.TempDir()
	writeFile(t, filepath.Join(homeDir, ".docker", "config.json"), `{"auths":{"ghcr.io":{},"reg.art.io":{},"other.io":{}}}`)

	registries, err := RegistriesToLogInto(homeDir, "art.io", []string{"reg-a.corp.example/x:1", "ghcr.io/vendor/tool:1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(registries.FromDockerAuthsOnly) != 0 {
		t.Errorf("FromDockerAuthsOnly = %v", registries.FromDockerAuthsOnly)
	}
	if len(registries.FromImages) != 0 {
		t.Errorf("FromImages = %v", registries.FromImages)
	}
}

func TestRunWithATwoLabelArtifactoryHostLogsInToNoRegistry(t *testing.T) {
	fixture := newFixture(t)
	fixture.dockerConfig = `{"auths":{"reg-a.corp.example":{},"ghcr.io":{},"other.io":{}}}`
	settings := fixture.settings()
	settings.ArtifactoryHost = "art.io"

	if _, err := Run(context.Background(), settings, fixture.runner, fixture.terminal()); err != nil {
		t.Fatal(err)
	}

	if got := fixture.loginCommands(); len(got) != 0 {
		t.Errorf("docker login commands = %v", got)
	}
}

func TestRegistriesToLogIntoSendsNothingToAnImageRegistryOutsideTheParentDomain(t *testing.T) {
	homeDir := t.TempDir()

	registries, err := RegistriesToLogInto(homeDir, "art.corp.example", []string{
		"reg-c.corp.example/x:1", "ghcr.io/vendor/tool:1", "docker.io/library/postgres:16", "evilcorp.example/x:1",
		"art.corp.example.evil.net/x:1", "10.0.0.5:5000/x:1", "corp.example/x:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"reg-c.corp.example"}; !slices.Equal(registries.FromImages, want) {
		t.Errorf("FromImages = %v, want %v", registries.FromImages, want)
	}
}

func TestRunNeverSendsTheTokenToAThirdPartyImageRegistry(t *testing.T) {
	fixture := newFixture(t)
	settings := fixture.settings()
	settings.ImageReferences = append(settings.ImageReferences, "ghcr.io/vendor/tool:1", "docker.io/library/postgres:16")

	changes, err := Run(context.Background(), settings, fixture.runner, fixture.terminal())
	if err != nil {
		t.Fatal(err)
	}

	if got := fixture.loginCommands(); !slices.Equal(got, []string{"docker login reg-a.corp.example --username " + testUser + " --password-stdin"}) {
		t.Errorf("docker login commands = %v", got)
	}
	if skipped := slices.IndexFunc(changes, func(change string) bool { return strings.HasPrefix(change, "skipped") }); skipped >= 0 {
		t.Errorf("a third-party image registry is listed as skipped: %v", changes)
	}
}

func (fixture *fixture) loginCommands() []string {
	var commands []string
	for _, call := range fixture.calls {
		if strings.HasPrefix(call, "docker login") {
			commands = append(commands, call)
		}
	}
	return commands
}
