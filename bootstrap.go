package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"dev/internal/appconfig"
	"dev/internal/bootstrap"
	"dev/internal/stack"
)

const (
	defaultArtifactoryHost = "artifactory.example.com"
	composeFileInRepo      = "devbox/docker-compose.yml"
)

type bootstrapCommand struct {
	ArtifactoryHost string `default:"${artifactory_host}" help:"Artifactory host for npm and jf."`
	NewToken        bool   `help:"Ask for a new Artifactory token, store it in pass and use it."`
}

func (command *bootstrapCommand) Run(ctx context.Context) error {
	currentWorkspace, err := loadCurrentWorkspace()
	if err != nil {
		return err
	}
	imageReferences, err := bootstrapImageReferences(currentWorkspace)
	if err != nil {
		return err
	}
	settings := bootstrap.Settings{HomeDir: currentWorkspace.homeDir, ArtifactoryHost: command.ArtifactoryHost, ImageReferences: imageReferences, ReplaceToken: command.NewToken}
	changes, err := bootstrap.Run(ctx, settings, runCommand, terminalOnStandardStreams{})
	for _, change := range changes {
		fmt.Println(change)
	}
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		fmt.Println("nothing to change")
	}
	return nil
}

func bootstrapImageReferences(currentWorkspace workspace) ([]string, error) {
	composeImages, err := bootstrap.ComposeImages(filepath.Join(currentWorkspace.repoPaths[appconfig.DefaultRepo], composeFileInRepo))
	if err != nil {
		return nil, err
	}
	mergedConfig, err := currentWorkspace.configForStack(stack.MasterStackName)
	if err != nil {
		return nil, err
	}
	return append(composeImages, appImageReferences(mergedConfig)...), nil
}

func appImageReferences(config appconfig.Config) []string {
	var imageReferences []string
	for _, appName := range slices.Sorted(maps.Keys(config.Apps)) {
		if image := config.Apps[appName].Container.Image; image != "" {
			imageReferences = append(imageReferences, image)
		}
	}
	return imageReferences
}

// The error of a command given a secret on stdin carries no stderr: a tool could echo what it read.
func runCommand(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = stdin
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		err = bootstrap.ExitError{Code: exitError.ExitCode()}
	}
	if stdin == nil && stderr.Len() > 0 {
		err = fmt.Errorf("%w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), err
}

type terminalOnStandardStreams struct{}

func (terminalOnStandardStreams) Read(buffer []byte) (int, error) {
	return os.Stdin.Read(buffer)
}

func (terminalOnStandardStreams) Write(content []byte) (int, error) {
	return os.Stderr.Write(content)
}

func (terminalOnStandardStreams) ReadSecretLine() (string, error) {
	hideTyping := exec.Command("stty", "-echo")
	hideTyping.Stdin = os.Stdin
	if err := hideTyping.Run(); err != nil {
		return "", errors.New("cannot read a secret without echo: run dev bootstrap in a terminal")
	}
	defer func() {
		showTyping := exec.Command("stty", "echo")
		showTyping.Stdin = os.Stdin
		_ = showTyping.Run()
	}()
	return bootstrap.ReadLine(os.Stdin)
}
