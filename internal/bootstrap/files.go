package bootstrap

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func WriteNpmrc(npmrcPath, artifactoryHost, token string) error {
	_, err := writeNpmrc(npmrcPath, artifactoryHost, token)
	return err
}

func writeNpmrc(npmrcPath, artifactoryHost, token string) (changed bool, err error) {
	if linkTarget, err := os.Readlink(npmrcPath); err == nil {
		return false, fmt.Errorf("%s is a symlink to %s: ilot bootstrap does not write through it, so put the token line there by hand", npmrcPath, linkTarget)
	}
	existingContent, err := readFileOrEmpty(npmrcPath)
	if err != nil {
		return false, err
	}
	tokenLine := fmt.Sprintf("//%s/artifactory/api/npm/:_authToken=%s", artifactoryHost, token)
	tokenLinePattern := regexp.MustCompile("^//" + regexp.QuoteMeta(artifactoryHost) + `(:[0-9]+)?/artifactory/api/npm/.*:_authToken=`)
	var otherLines, replacedTokenLines []string
	for _, line := range strings.Split(strings.TrimSuffix(existingContent, "\n"), "\n") {
		switch {
		case tokenLinePattern.MatchString(line):
			replacedTokenLines = append(replacedTokenLines, line)
		case line != "":
			otherLines = append(otherLines, line)
		}
	}
	newContent := existingContent
	if len(replacedTokenLines) != 1 || replacedTokenLines[0] != tokenLine {
		newContent = strings.Join(append(otherLines, tokenLine), "\n") + "\n"
	}
	modeIsPrivate := isPrivateFile(npmrcPath)
	if newContent == existingContent && modeIsPrivate {
		return false, nil
	}
	return true, replaceFileAtomically(npmrcPath, []byte(newContent))
}

func replaceFileAtomically(path string, content []byte) error {
	tempFile, err := os.CreateTemp(filepath.Dir(path), ".npmrc-*")
	if err != nil {
		return err
	}
	defer os.Remove(tempFile.Name())
	if err := tempFile.Chmod(0o600); err != nil {
		tempFile.Close()
		return err
	}
	if _, err := tempFile.Write(content); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Sync(); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	return os.Rename(tempFile.Name(), path)
}

func EnsureLines(configPath string, lines []string) error {
	_, err := ensureLines(configPath, lines)
	return err
}

func ensureLines(configPath string, lines []string) (changed bool, err error) {
	existingContent, err := readFileOrEmpty(configPath)
	if err != nil {
		return false, err
	}
	presentLines := map[string]bool{}
	for _, line := range strings.Split(existingContent, "\n") {
		presentLines[strings.TrimSpace(line)] = true
	}
	var missingLines []string
	for _, line := range lines {
		if !presentLines[strings.TrimSpace(line)] {
			missingLines = append(missingLines, line)
		}
	}
	if len(missingLines) == 0 {
		return false, nil
	}
	newContent := existingContent
	if newContent != "" && !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}
	newContent += strings.Join(missingLines, "\n") + "\n"
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return false, err
	}
	return true, os.WriteFile(configPath, []byte(newContent), 0o600)
}

func readFileOrEmpty(path string) (string, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(content), err
}

func isPrivateFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().Perm() == 0o600
}
