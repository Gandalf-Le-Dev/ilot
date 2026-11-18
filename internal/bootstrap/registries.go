package bootstrap

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

func RegistryHostOfImage(imageReference string) string {
	host, _, hasPath := strings.Cut(imageReference, "/")
	if !hasPath {
		return ""
	}
	if host == "localhost" || strings.HasPrefix(host, "localhost:") {
		return ""
	}
	if strings.ContainsAny(host, ".:") {
		return host
	}
	return ""
}

type Registries struct {
	FromImages          []string
	FromDockerAuthsOnly []string
}

func RegistriesToLogInto(homeDir, artifactoryHost string, imageReferences []string) (Registries, error) {
	registriesOfImages := map[string]bool{}
	for _, imageReference := range imageReferences {
		if host := RegistryHostOfImage(imageReference); host != "" && isUnderParentDomain(host, artifactoryHost) {
			registriesOfImages[host] = true
		}
	}
	dockerConfig, err := readDockerConfig(dockerConfigPath(homeDir))
	if err != nil {
		return Registries{FromImages: nil, FromDockerAuthsOnly: nil}, err
	}
	registriesOfAuthsOnly := map[string]bool{}
	for registry := range dockerAuths(dockerConfig) {
		if !registriesOfImages[registry] && isUnderParentDomain(registry, artifactoryHost) {
			registriesOfAuthsOnly[registry] = true
		}
	}
	return Registries{
		FromImages:          slices.Sorted(maps.Keys(registriesOfImages)),
		FromDockerAuthsOnly: slices.Sorted(maps.Keys(registriesOfAuthsOnly)),
	}, nil
}

// The corp token goes only to hosts of the corp domain: any other host is another party's registry.
func isUnderParentDomain(registryOrAuthsKey, artifactoryHost string) bool {
	_, parentDomain, hasParent := strings.Cut(artifactoryHost, ".")
	if !hasParent || !strings.Contains(parentDomain, ".") || net.ParseIP(artifactoryHost) != nil {
		return false
	}
	host := strings.ToLower(hostOfAuthsKey(registryOrAuthsKey))
	return net.ParseIP(host) == nil && strings.HasSuffix(host, "."+strings.ToLower(parentDomain))
}

func hostOfAuthsKey(authsKey string) string {
	withoutScheme := authsKey
	if _, afterScheme, hasScheme := strings.Cut(authsKey, "://"); hasScheme {
		withoutScheme = afterScheme
	}
	hostAndPort, _, _ := strings.Cut(withoutScheme, "/")
	if host, _, err := net.SplitHostPort(hostAndPort); err == nil {
		return host
	}
	return strings.Trim(hostAndPort, "[]")
}

func dockerAuths(dockerConfig map[string]any) map[string]any {
	auths, _ := dockerConfig["auths"].(map[string]any)
	return auths
}

func registriesHoldingInlineCredentials(dockerConfig map[string]any) []string {
	var registries []string
	for registry, entry := range dockerAuths(dockerConfig) {
		if fields, isObject := entry.(map[string]any); isObject {
			if inlineAuth, _ := fields["auth"].(string); inlineAuth != "" {
				registries = append(registries, registry)
			}
		}
	}
	slices.Sort(registries)
	return registries
}

func ComposeImages(composePath string) ([]string, error) {
	content, err := os.ReadFile(composePath)
	if err != nil {
		return nil, err
	}
	var compose struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(content, &compose); err != nil {
		return nil, err
	}
	var images []string
	for _, serviceName := range slices.Sorted(maps.Keys(compose.Services)) {
		if image := compose.Services[serviceName].Image; image != "" {
			images = append(images, image)
		}
	}
	return images, nil
}

func dockerConfigPath(homeDir string) string {
	return filepath.Join(homeDir, ".docker", "config.json")
}

func readDockerConfig(configPath string) (map[string]any, error) {
	content, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	dockerConfig := map[string]any{}
	if err := json.Unmarshal(content, &dockerConfig); err != nil {
		return nil, err
	}
	return dockerConfig, nil
}
