package main

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed skill/SKILL.md
var agentSkill string

type skillCommand struct {
	Print   skillPrintCommand   `cmd:"" help:"Print the ilot skill for AI agents."`
	Install skillInstallCommand `cmd:"" help:"Copy the ilot skill to ~/.claude/skills/ilot/SKILL.md, for Claude Code."`
}

type skillPrintCommand struct{}

func (skillPrintCommand) Run() error {
	_, err := fmt.Print(agentSkill)
	return err
}

type skillInstallCommand struct{}

func (skillInstallCommand) Run() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	skillFile, err := installAgentSkill(homeDir)
	if err != nil {
		return fmt.Errorf("install the ilot skill: %w", err)
	}
	fmt.Println("the ilot skill is in " + skillFile)
	return nil
}

func installAgentSkill(homeDir string) (skillFile string, err error) {
	skillFile = filepath.Join(homeDir, ".claude", "skills", "ilot", "SKILL.md")
	installed, err := os.ReadFile(skillFile)
	if err == nil && bytes.Equal(installed, []byte(agentSkill)) {
		return skillFile, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(skillFile), 0o755); err != nil {
		return "", err
	}
	return skillFile, os.WriteFile(skillFile, []byte(agentSkill), 0o644)
}
