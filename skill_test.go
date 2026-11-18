package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func installedSkillPath(homeDir string) string {
	return filepath.Join(homeDir, ".claude", "skills", "dev", "SKILL.md")
}

func TestInstallAgentSkillWritesTheEmbeddedFile(t *testing.T) {
	homeDir := t.TempDir()

	if err := installAgentSkill(homeDir); err != nil {
		t.Fatalf("installAgentSkill: %v", err)
	}

	content, err := os.ReadFile(installedSkillPath(homeDir))
	if err != nil || string(content) != agentSkill {
		t.Fatalf("installed skill = %d bytes, %v; want the embedded skill", len(content), err)
	}
}

func TestInstallAgentSkillLeavesAnIdenticalFileAlone(t *testing.T) {
	homeDir := t.TempDir()
	if err := installAgentSkill(homeDir); err != nil {
		t.Fatal(err)
	}
	longAgo := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(installedSkillPath(homeDir), longAgo, longAgo); err != nil {
		t.Fatal(err)
	}

	if err := installAgentSkill(homeDir); err != nil {
		t.Fatalf("installAgentSkill: %v", err)
	}

	info, err := os.Stat(installedSkillPath(homeDir))
	if err != nil || !info.ModTime().Equal(longAgo) {
		t.Fatalf("skill modified at %v, %v; want it left alone", info.ModTime(), err)
	}
}

func TestInstallAgentSkillKeepsOtherFiles(t *testing.T) {
	homeDir := t.TempDir()
	userNote := filepath.Join(filepath.Dir(installedSkillPath(homeDir)), "notes.md")
	if err := os.MkdirAll(filepath.Dir(userNote), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userNote, []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := installAgentSkill(homeDir); err != nil {
		t.Fatalf("installAgentSkill: %v", err)
	}

	if content, err := os.ReadFile(userNote); err != nil || string(content) != "mine\n" {
		t.Fatalf("user note = %q, %v; want it kept", content, err)
	}
}

func TestAgentSkillPreApprovesEveryDevCommand(t *testing.T) {
	frontmatter, _, isFound := strings.Cut(strings.TrimPrefix(agentSkill, "---\n"), "\n---\n")
	if !strings.HasPrefix(agentSkill, "---\n") || !isFound {
		t.Fatalf("skill has no frontmatter:\n%s", agentSkill)
	}
	for _, wantLine := range []string{"name: dev", "allowed-tools: Bash(dev:*)"} {
		if !strings.Contains(frontmatter, wantLine) {
			t.Errorf("frontmatter lacks %q:\n%s", wantLine, frontmatter)
		}
	}
}

func TestAgentSkillNamesEveryCommand(t *testing.T) {
	commandFields := reflect.TypeFor[cli]()
	for fieldIndex := range commandFields.NumField() {
		field := commandFields.Field(fieldIndex)
		if _, isCommand := field.Tag.Lookup("cmd"); !isCommand {
			continue
		}
		if commandLine := "dev " + strings.ToLower(field.Name); !strings.Contains(agentSkill, commandLine) {
			t.Errorf("the skill never names %q", commandLine)
		}
	}
}

func TestInstallAgentSkillOrWarnPrintsOneWarningWhenTheFolderCannotBeWritten(t *testing.T) {
	homeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(homeDir, ".claude"), []byte("a file where the folder must be\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warnings strings.Builder

	installAgentSkillOrWarn(func() (string, error) { return homeDir, nil }, &warnings)

	if lines := strings.Split(strings.TrimSpace(warnings.String()), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], "warning: install the dev skill: ") {
		t.Fatalf("warnings = %q, want one warning line", warnings.String())
	}
}

func TestInstallAgentSkillOrWarnPrintsOneWarningWithoutAHomeFolder(t *testing.T) {
	var warnings strings.Builder

	installAgentSkillOrWarn(func() (string, error) { return "", errors.New("$HOME is not defined") }, &warnings)

	if warnings.String() != "warning: install the dev skill: $HOME is not defined\n" {
		t.Fatalf("warnings = %q, want one warning naming the cause", warnings.String())
	}
}
