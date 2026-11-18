package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	tokenPassEntry = "devbox/artifactory-token"
	userPassEntry  = "devbox/artifactory-user"
)

var (
	gpgConfLines      = []string{"pinentry-mode loopback"}
	gpgAgentConfLines = []string{"allow-loopback-pinentry", "default-cache-ttl 34560000", "max-cache-ttl 34560000"}
)

type Settings struct {
	HomeDir         string
	ArtifactoryHost string
	ImageReferences []string
	ReplaceToken    bool
}

type Runner func(ctx context.Context, stdin io.Reader, name string, args ...string) (stdout []byte, err error)

type ExitError struct {
	Code int
}

func (err ExitError) Error() string {
	return fmt.Sprintf("exit status %d", err.Code)
}

// A terminal that can read without echo implements it; any other terminal is read as it is.
type SecretReader interface {
	ReadSecretLine() (string, error)
}

type bootstrapRun struct {
	ctx                context.Context
	settings           Settings
	runner             Runner
	terminal           io.ReadWriter
	signingFingerprint string
	token              string
	user               string
	changes            []string
}

// Unlock comes before the pass entries: with loopback only, pass cannot read an entry while the agent holds no passphrase.
func Run(ctx context.Context, settings Settings, runner Runner, terminal io.ReadWriter) (changes []string, err error) {
	state := &bootstrapRun{ctx: ctx, settings: settings, runner: runner, terminal: terminal, signingFingerprint: "", token: "", user: "", changes: nil}
	steps := []func() error{
		state.checkPassUsesTheSigningKey,
		state.ensureGnupgConfig,
		state.unlockAgents,
		state.loadOrAskCredentials,
		state.writeNpmrc,
		state.configureJFrog,
		state.loginToDockerRegistries,
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return state.changes, err
		}
	}
	return state.changes, nil
}

func (state *bootstrapRun) record(change string) {
	state.changes = append(state.changes, change)
}

func (state *bootstrapRun) homePath(elements ...string) string {
	return filepath.Join(append([]string{state.settings.HomeDir}, elements...)...)
}

func (state *bootstrapRun) run(stdin io.Reader, name string, args ...string) ([]byte, error) {
	output, err := state.runner(state.ctx, stdin, name, args...)
	if err != nil {
		return output, fmt.Errorf("%s: %w", strings.Join(append([]string{name}, args...), " "), err)
	}
	return output, nil
}

func (state *bootstrapRun) checkPassUsesTheSigningKey() error {
	signingKeyOutput, err := state.run(nil, "git", "config", "user.signingkey")
	signingKey := strings.TrimSpace(string(signingKeyOutput))
	if err != nil || signingKey == "" {
		return errors.New("git user.signingkey is not set: set it to the key that signs your commits, then run dev bootstrap again")
	}
	signingFingerprints, err := state.primaryFingerprints(signingKey)
	if err != nil {
		return err
	}
	if len(signingFingerprints) != 1 {
		return fmt.Errorf("git user.signingkey %q matches %d keys: set it to a long key id or a fingerprint", signingKey, len(signingFingerprints))
	}
	state.signingFingerprint = signingFingerprints[0]

	passKeysContent, err := os.ReadFile(state.homePath(".password-store", ".gpg-id"))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("pass is not initialised: run `pass init %s`", signingKey)
	}
	if err != nil {
		return err
	}
	for _, passKey := range strings.Fields(string(passKeysContent)) {
		passKeyFingerprints, err := state.primaryFingerprints(passKey)
		if err != nil {
			return err
		}
		if len(passKeyFingerprints) != 1 || passKeyFingerprints[0] != state.signingFingerprint {
			return fmt.Errorf("pass encrypts for %s, which is not the key git signs with: run `pass init %s`", passKey, signingKey)
		}
	}
	return nil
}

func (state *bootstrapRun) primaryFingerprints(keySpecifier string) ([]string, error) {
	output, err := state.run(nil, "gpg", "--list-keys", "--with-colons", keySpecifier)
	if err != nil {
		return nil, err
	}
	var fingerprints []string
	isAfterPrimaryKey := false
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Split(line, ":")
		switch fields[0] {
		case "pub":
			isAfterPrimaryKey = true
		case "sub":
			isAfterPrimaryKey = false
		case "fpr":
			if isAfterPrimaryKey && len(fields) > 9 {
				fingerprints = append(fingerprints, fields[9])
			}
			isAfterPrimaryKey = false
		}
	}
	return fingerprints, nil
}

func (state *bootstrapRun) ensureGnupgConfig() error {
	gnupgDir := state.homePath(".gnupg")
	if changed, err := ensureLines(filepath.Join(gnupgDir, "gpg.conf"), gpgConfLines); err != nil {
		return err
	} else if changed {
		state.record("added loopback pinentry to ~/.gnupg/gpg.conf")
	}
	changed, err := ensureLines(filepath.Join(gnupgDir, "gpg-agent.conf"), gpgAgentConfLines)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	state.record("added loopback and cache lines to ~/.gnupg/gpg-agent.conf")
	if _, err := state.run(nil, "gpg-connect-agent", "reloadagent", "/bye"); err != nil {
		return err
	}
	state.record("reloaded gpg-agent")
	return nil
}

func (state *bootstrapRun) unlockAgents() error {
	_, err := state.run(nil, "ssh-add", "-l")
	var exitError ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitError) && exitError.Code == 1:
		if _, err := state.run(nil, "ssh-add"); err != nil {
			if !state.holdsAPrivateSshKey() {
				return fmt.Errorf(`%w: ~/.ssh holds no private key for ssh-add: put the devhost SSH key that your Git server knows at ~/.ssh/id_ed25519 (~/devbox/.ssh/id_ed25519 on the devhost), see the setup docs, section "The devhost SSH key"`, err)
			}
			return err
		}
		state.record("loaded the ssh key into ssh-agent")
	default:
		return fmt.Errorf("%w: is ssh-agent running at $SSH_AUTH_SOCK?", err)
	}
	return state.unlockGpgAgent()
}

func (state *bootstrapRun) holdsAPrivateSshKey() bool {
	keyFiles, err := filepath.Glob(state.homePath(".ssh", "id_*"))
	if err != nil {
		return false
	}
	return slices.ContainsFunc(keyFiles, func(keyFile string) bool { return !strings.HasSuffix(keyFile, ".pub") })
}

// gpg-agent caches a passphrase per key, and pass decrypts with another key than the one that signs.
func (state *bootstrapRun) unlockGpgAgent() error {
	probeFolder, err := os.MkdirTemp("", "dev-bootstrap-probe-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(probeFolder)
	fileToSign := filepath.Join(probeFolder, "plain")
	encryptedProbe := filepath.Join(probeFolder, "probe.gpg")
	if err := os.WriteFile(fileToSign, []byte("dev bootstrap\n"), 0o600); err != nil {
		return err
	}
	if _, err := state.run(nil, "gpg", "--batch", "--yes", "--trust-model", "always", "--recipient", state.signingFingerprint, "--output", encryptedProbe, "--encrypt", fileToSign); err != nil {
		return err
	}
	signArguments := func(pinentryMode string, extraArguments ...string) []string {
		return append(append([]string{"--batch", "--pinentry-mode", pinentryMode}, extraArguments...),
			"--local-user", state.signingFingerprint, "--output", "-", "--clearsign", fileToSign)
	}
	decryptArguments := func(pinentryMode string, extraArguments ...string) []string {
		return append(append([]string{"--batch", "--pinentry-mode", pinentryMode}, extraArguments...),
			"--output", "-", "--decrypt", encryptedProbe)
	}
	_, signingIsLocked := state.run(nil, "gpg", signArguments("error")...)
	_, decryptionIsLocked := state.run(nil, "gpg", decryptArguments("error")...)
	if signingIsLocked == nil && decryptionIsLocked == nil {
		return nil
	}
	passphrase, err := state.ask("GPG passphrase: ", true)
	if err != nil {
		return err
	}
	if signingIsLocked != nil {
		if _, err := state.run(strings.NewReader(passphrase+"\n"), "gpg", signArguments("loopback", "--passphrase-fd", "0")...); err != nil {
			return fmt.Errorf("the test signature failed, the passphrase may be wrong: %w", err)
		}
	}
	if decryptionIsLocked != nil {
		if _, err := state.run(strings.NewReader(passphrase+"\n"), "gpg", decryptArguments("loopback", "--passphrase-fd", "0")...); err != nil {
			return fmt.Errorf("the test decryption failed, the passphrase may be wrong: %w", err)
		}
	}
	state.record("unlocked gpg-agent with a loopback test signature and decryption")
	return nil
}

func (state *bootstrapRun) ask(prompt string, isSecret bool) (string, error) {
	if _, err := io.WriteString(state.terminal, prompt); err != nil {
		return "", err
	}
	var answer string
	var err error
	if secretReader, canReadSecret := state.terminal.(SecretReader); canReadSecret && isSecret {
		answer, err = secretReader.ReadSecretLine()
	} else {
		answer, err = ReadLine(state.terminal)
	}
	if isSecret {
		_, _ = io.WriteString(state.terminal, "\n")
	}
	answer = strings.TrimSpace(answer)
	if err != nil && !(errors.Is(err, io.EOF) && answer != "") {
		return "", err
	}
	if answer == "" {
		return "", errors.New("no answer given")
	}
	return answer, nil
}

// Reads byte by byte so that nothing after the newline is consumed.
func ReadLine(reader io.Reader) (string, error) {
	var line []byte
	oneByte := make([]byte, 1)
	for {
		count, err := reader.Read(oneByte)
		if count == 1 {
			if oneByte[0] == '\n' {
				return string(line), nil
			}
			line = append(line, oneByte[0])
		}
		if err != nil {
			return string(line), err
		}
	}
}

func (state *bootstrapRun) loadOrAskCredentials() error {
	var err error
	if state.settings.ReplaceToken {
		state.token, err = state.askAndStore(tokenPassEntry, "New Artifactory token (not shown): ", true)
	} else {
		state.token, err = state.passValueOrAsk(tokenPassEntry, "Artifactory token (not shown): ", true)
	}
	if err != nil {
		return err
	}
	state.user, err = state.passValueOrAsk(userPassEntry, "Artifactory user (your login email): ", false)
	return err
}

func (state *bootstrapRun) passValueOrAsk(entry, prompt string, isSecret bool) (string, error) {
	output, err := state.run(nil, "pass", "show", entry)
	if err == nil {
		firstLine, _, _ := strings.Cut(string(output), "\n")
		return strings.TrimSpace(firstLine), nil
	}
	if _, statErr := os.Stat(state.homePath(".password-store", entry+".gpg")); statErr == nil {
		return "", err
	}
	return state.askAndStore(entry, prompt, isSecret)
}

func (state *bootstrapRun) askAndStore(entry, prompt string, isSecret bool) (string, error) {
	value, err := state.ask(prompt, isSecret)
	if err != nil {
		return "", err
	}
	if _, err := state.run(strings.NewReader(value+"\n"), "pass", "insert", "--multiline", "--force", entry); err != nil {
		return "", err
	}
	state.record("stored " + entry + " in pass")
	return value, nil
}

func (state *bootstrapRun) writeNpmrc() error {
	changed, err := writeNpmrc(state.homePath(".npmrc"), state.settings.ArtifactoryHost, state.token)
	if err != nil {
		return err
	}
	if changed {
		state.record("wrote the Artifactory token to ~/.npmrc (mode 600)")
	}
	return nil
}

func (state *bootstrapRun) configureJFrog() error {
	if state.jfrogHoldsTheToken() {
		return nil
	}
	artifactoryURL := "https://" + state.settings.ArtifactoryHost
	_, err := state.run(strings.NewReader(state.token+"\n"), "jf", "config", "add", state.settings.ArtifactoryHost,
		"--url", artifactoryURL, "--artifactory-url", artifactoryURL+"/artifactory",
		"--access-token-stdin", "--interactive=false", "--overwrite")
	if err != nil {
		return err
	}
	state.record("configured jf server " + state.settings.ArtifactoryHost)
	return nil
}

func (state *bootstrapRun) jfrogHoldsTheToken() bool {
	configFiles, err := filepath.Glob(state.homePath(".jfrog", "jfrog-cli.conf*"))
	if err != nil {
		return false
	}
	for _, configFile := range configFiles {
		content, err := os.ReadFile(configFile)
		if err != nil {
			continue
		}
		var jfrogConfig struct {
			Servers []struct {
				ServerID    string `json:"serverId"`
				AccessToken string `json:"accessToken"`
			} `json:"servers"`
		}
		if json.Unmarshal(content, &jfrogConfig) != nil {
			continue
		}
		for _, server := range jfrogConfig.Servers {
			if server.ServerID == state.settings.ArtifactoryHost && server.AccessToken == state.token {
				return true
			}
		}
	}
	return false
}

func (state *bootstrapRun) loginToDockerRegistries() error {
	if err := state.setDockerCredentialStore(); err != nil {
		return err
	}
	registries, err := RegistriesToLogInto(state.settings.HomeDir, state.settings.ArtifactoryHost, state.settings.ImageReferences)
	if err != nil {
		return err
	}
	registriesAlreadyLoggedIn := state.dockerRegistriesHoldingTheUser()
	login := func(registry string) error {
		_, err := state.run(strings.NewReader(state.token+"\n"), "docker", "login", registry, "--username", state.user, "--password-stdin")
		if err == nil && !slices.Contains(registriesAlreadyLoggedIn, registry) {
			state.record("logged docker in to " + registry)
		}
		return err
	}
	for _, registry := range registries.FromImages {
		if err := login(registry); err != nil {
			return err
		}
	}
	for _, registry := range registries.FromDockerAuthsOnly {
		if err := login(registry); err != nil {
			state.record("skipped docker login to " + registry)
		}
	}
	return nil
}

func (state *bootstrapRun) setDockerCredentialStore() error {
	configPath := dockerConfigPath(state.settings.HomeDir)
	dockerConfig, err := readDockerConfig(configPath)
	if err != nil {
		return err
	}
	if dockerConfig["credsStore"] == "pass" {
		return nil
	}
	if registries := registriesHoldingInlineCredentials(dockerConfig); len(registries) > 0 {
		fmt.Fprintf(state.terminal, "warning: credsStore pass hides the inline docker credentials of %s: dev bootstrap logs in again, and skips the registries it cannot log in to\n", strings.Join(registries, ", "))
	}
	dockerConfig["credsStore"] = "pass"
	content, err := json.MarshalIndent(dockerConfig, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(configPath, append(content, '\n'), 0o600); err != nil {
		return err
	}
	state.record("set credsStore to pass in ~/.docker/config.json")
	return nil
}

func (state *bootstrapRun) dockerRegistriesHoldingTheUser() []string {
	output, err := state.runner(state.ctx, nil, "docker-credential-pass", "list")
	if err != nil {
		return nil
	}
	userByServerURL := map[string]string{}
	if json.Unmarshal(output, &userByServerURL) != nil {
		return nil
	}
	var registries []string
	for serverURL, user := range userByServerURL {
		if user == state.user {
			registries = append(registries, strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(serverURL, "https://"), "http://"), "/"))
		}
	}
	return registries
}
