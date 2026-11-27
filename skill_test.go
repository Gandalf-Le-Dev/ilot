package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
)

func installedSkillPath(homeDir string) string {
	return filepath.Join(homeDir, ".claude", "skills", "ilot", "SKILL.md")
}

func TestInstallAgentSkillWritesTheEmbeddedFile(t *testing.T) {
	homeDir := t.TempDir()

	if _, err := installAgentSkill(homeDir); err != nil {
		t.Fatalf("installAgentSkill: %v", err)
	}

	content, err := os.ReadFile(installedSkillPath(homeDir))
	if err != nil || string(content) != agentSkill {
		t.Fatalf("installed skill = %d bytes, %v; want the embedded skill", len(content), err)
	}
}

func TestInstallAgentSkillLeavesAnIdenticalFileAlone(t *testing.T) {
	homeDir := t.TempDir()
	if _, err := installAgentSkill(homeDir); err != nil {
		t.Fatal(err)
	}
	longAgo := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(installedSkillPath(homeDir), longAgo, longAgo); err != nil {
		t.Fatal(err)
	}

	if _, err := installAgentSkill(homeDir); err != nil {
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

	if _, err := installAgentSkill(homeDir); err != nil {
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
	for _, wantLine := range []string{"name: ilot", "allowed-tools: Bash(ilot:*)"} {
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
		if commandLine := "ilot " + strings.ToLower(field.Name); !strings.Contains(agentSkill, commandLine) {
			t.Errorf("the skill never names %q", commandLine)
		}
	}
}

func TestInstallAgentSkillFailsWhenTheFolderCannotBeWritten(t *testing.T) {
	homeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(homeDir, ".claude"), []byte("a file where the folder must be\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := installAgentSkill(homeDir); err == nil {
		t.Fatal("installAgentSkill = nil, want an error")
	}
}

func TestSkillHasAPrintAndAnInstallCommand(t *testing.T) {
	for _, subcommand := range []string{"print", "install"} {
		parser, err := kong.New(&cli{}, kong.Vars{"version": "test"})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.Parse([]string{"skill", subcommand})
		if err != nil || parsed.Command() != "skill "+subcommand {
			t.Errorf("parse skill %s = %v, %v", subcommand, parsed, err)
		}
	}
}
