package main

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
)

//go:embed skill/SKILL.md
var agentSkill string

type skillFlag bool

func (skillFlag) BeforeApply(app *kong.Kong) error {
	fmt.Fprint(app.Stdout, agentSkill)
	app.Exit(0)
	return nil
}

func installAgentSkill(homeDir string) error {
	skillFile := filepath.Join(homeDir, ".claude", "skills", "ilot", "SKILL.md")
	installed, err := os.ReadFile(skillFile)
	if err == nil && bytes.Equal(installed, []byte(agentSkill)) {
		return nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(skillFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(skillFile, []byte(agentSkill), 0o644)
}

// The install never stops ilot boot: without the skill, the apps still have to start.
func installAgentSkillOrWarn(homeDirOf func() (string, error), warnings io.Writer) {
	homeDir, err := homeDirOf()
	if err == nil {
		err = installAgentSkill(homeDir)
	}
	if err != nil {
		fmt.Fprintf(warnings, "warning: install the ilot skill: %v\n", err)
	}
}
