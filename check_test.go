package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	configFile := filepath.Join(t.TempDir(), "ilot.yaml")
	if err := os.WriteFile(configFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return configFile
}

func TestCheckConfigFileFindsAnAppDeclaredInTwoFiles(t *testing.T) {
	platformFile := writeConfigFile(t, "domain: dev.example.net\napps:\n  shared-name: {run: one}\n")
	portalFile := writeConfigFile(t, "apps:\n  shared-name: {run: two}\n")

	problems := checkConfigFile("portal", portalFile, map[string]string{"platform": platformFile})

	joined := errors.Join(problems...)
	if joined == nil || !strings.Contains(joined.Error(), "platform") || !strings.Contains(joined.Error(), "portal") {
		t.Fatalf("checkConfigFile = %v, want a problem naming both repos", joined)
	}
}

func TestCheckWithoutAPathUsesTheRepoOfTheCurrentFolder(t *testing.T) {
	portalDir := gitRepoWithFiles(t, map[string]string{"ilot.yaml": "apps:\n  portal-api: {run: api}\n"})
	platformDir := gitRepoWithFiles(t, map[string]string{
		"devbox/ilot.yaml": "domain: dev.example.net\nrepos:\n  portal: {path: /nowhere, config: ilot.yaml}\napps:\n  standalone: {run: standalone}\n",
	})
	homeDir := homeWithPersonalFile(t, "repos:\n  platform: {path: "+platformDir+"}\n  portal: {path: "+portalDir+"}\n")
	currentWorkspace, err := loadWorkspace(homeDir)
	if err != nil {
		t.Fatal(err)
	}

	repoName, configFile, err := configFileToCheck(&currentWorkspace, portalDir, "")

	if err != nil || repoName != "portal" || configFile != filepath.Join(portalDir, "ilot.yaml") {
		t.Fatalf("configFileToCheck = %q, %q, %v; want portal and its ilot.yaml", repoName, configFile, err)
	}
}

func TestCheckOfAFileOutsideAnyCheckoutNeedsARepoList(t *testing.T) {
	configFile := writeConfigFile(t, "apps:\n  lonely: {run: lonely}\n")

	_, _, err := configFileToCheck(nil, filepath.Dir(configFile), configFile)

	if err == nil || !strings.Contains(err.Error(), "repos:") {
		t.Fatalf("configFileToCheck = %v, want an error that asks for a repos: list", err)
	}
}

func TestCheckConfigFileNamesTheFileThatHoldsTheProblem(t *testing.T) {
	platformFile := writeConfigFile(t, "domain: dev.example.net\napps:\n  gateway: {run: gw, gateway: true, ports: [http]}\n")
	portalFile := writeConfigFile(t, "apps:\n  portal-api: {run: 'api --port={{port \"gateway\" \"htp\"}}'}\n")

	problems := checkConfigFile("platform", platformFile, map[string]string{"portal": portalFile})

	joined := errors.Join(problems...)
	if joined == nil || !strings.Contains(joined.Error(), portalFile) || strings.Contains(joined.Error(), platformFile) {
		t.Fatalf("checkConfigFile = %v, want the problem on %s only", joined, portalFile)
	}
}

func TestCheckConfigFileStillRendersWhenAValidationRuleFails(t *testing.T) {
	configFile := writeConfigFile(t, "domain: dev.example.net\napps:\n"+
		"  first: {run: one, ports: [http], route: http, gateway_aliases: [same-alias]}\n"+
		"  second: {run: 'two --port={{port \"ghost\" \"http\"}}', ports: [http], route: http, gateway_aliases: [same-alias]}\n")

	problems := checkConfigFile("platform", configFile, nil)

	joined := errors.Join(problems...)
	if joined == nil || !strings.Contains(joined.Error(), "same-alias") || !strings.Contains(joined.Error(), "ghost") {
		t.Fatalf("checkConfigFile = %v, want both the alias claimed twice and the template typo", joined)
	}
}

func TestCheckWithoutAPathOutsideACheckoutUsesPlatformsMainCheckout(t *testing.T) {
	platformDir := gitRepoWithFiles(t, map[string]string{
		"devbox/ilot.yaml": "domain: dev.example.net\napps:\n  standalone: {run: standalone}\n",
	})
	homeDir := homeWithPersonalFile(t, "repos:\n  platform: {path: "+platformDir+"}\n")
	currentWorkspace, err := loadWorkspace(homeDir)
	if err != nil {
		t.Fatal(err)
	}

	repoName, configFile, err := configFileToCheck(&currentWorkspace, t.TempDir(), "")

	if err != nil || repoName != "platform" || configFile != filepath.Join(platformDir, "devbox", "ilot.yaml") {
		t.Fatalf("configFileToCheck = %q, %q, %v; want platform's file in its main checkout", repoName, configFile, err)
	}
}

func TestCheckWithoutAPathFromAHomeFolderThatIsACheckoutUsesPlatformsMainCheckout(t *testing.T) {
	platformDir := gitRepoWithFiles(t, map[string]string{
		"devbox/ilot.yaml": "domain: dev.example.net\napps:\n  standalone: {run: standalone}\n",
	})
	homeDir := homeWithPersonalFile(t, "repos:\n  platform: {path: "+platformDir+"}\n")
	runGitInTest(t, homeDir, "init", "-q", "-b", "master")
	runGitInTest(t, homeDir, "commit", "-q", "--allow-empty", "-m", "dotfiles")
	currentWorkspace, err := loadWorkspace(homeDir)
	if err != nil {
		t.Fatal(err)
	}

	repoName, configFile, err := configFileToCheck(&currentWorkspace, homeDir, "")

	if err != nil || repoName != "platform" || configFile != filepath.Join(platformDir, "devbox", "ilot.yaml") {
		t.Fatalf("configFileToCheck = %q, %q, %v; want platform's file in its main checkout", repoName, configFile, err)
	}
}
