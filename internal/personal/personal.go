package personal

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/Gandalf-Le-Dev/ilot/internal/appconfig"
)

type Settings struct {
	Env   map[string]string               `yaml:"env"`
	Apps  map[string]map[string]string    `yaml:"apps"`
	Repos map[string]appconfig.RepoConfig `yaml:"repos"`
}

func Load(settingsPath string) (Settings, error) {
	content, err := os.ReadFile(settingsPath)
	if errors.Is(err, fs.ErrNotExist) {
		return Settings{Env: nil, Apps: nil, Repos: nil}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	var settings Settings
	if err := decoder.Decode(&settings); err != nil && !errors.Is(err, io.EOF) {
		return Settings{}, fmt.Errorf("read %s: %w", settingsPath, err)
	}
	return settings, nil
}
