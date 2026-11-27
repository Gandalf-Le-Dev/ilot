package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/workspace"
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
	platformFile := writeConfigFile(t, "apps:\n  shared-name: {run: one}\n")
	portalFile := writeConfigFile(t, "apps:\n  shared-name: {run: two}\n")

	problems, _ := checkConfigFile("platform", []string{"platform", "portal"}, testGlobal.Ports, "portal", portalFile, map[string]string{"platform": platformFile})

	joined := errors.Join(problems...)
	if joined == nil || !strings.Contains(joined.Error(), "platform") || !strings.Contains(joined.Error(), "portal") {
		t.Fatalf("checkConfigFile = %v, want a problem naming both repos", joined)
	}
}

func shopAndWebForCheck(t *testing.T) (shopDir, webDir string) {
	t.Helper()
	codeDir := canonicalTempDir(t)
	shopDir = gitRepoAt(t, filepath.Join(codeDir, "shop"), map[string]string{"ilot.yaml": "repos:\n  web: ../web\napps:\n  api: {run: api, ports: [http]}\n"})
	webDir = gitRepoAt(t, filepath.Join(codeDir, "web"), map[string]string{"ilot.yaml": "member_of: ../shop\napps:\n  front: {run: 'front --api={{port \"api\"}}'}\n"})
	return shopDir, webDir
}

func TestCheckWithoutAPathChecksTheFileOfTheCheckoutWithTheFilesOfTheOtherRepos(t *testing.T) {
	shopDir, webDir := shopAndWebForCheck(t)
	currentWorkspace, checkout, err := workspace.Find(webDir, t.TempDir(), testGlobal, workspace.NewGit())
	if err != nil {
		t.Fatal(err)
	}

	checkedFile, otherFileByRepo, err := configFilesToCheck(currentWorkspace, checkout, "")

	if err != nil || checkedFile != filepath.Join(webDir, "ilot.yaml") || len(otherFileByRepo) != 1 || otherFileByRepo["shop"] != filepath.Join(shopDir, "ilot.yaml") {
		t.Fatalf("configFilesToCheck = %q, %v, %v; want the file of web with the file of shop", checkedFile, otherFileByRepo, err)
	}
	if problems, _ := checkConfigFile(currentWorkspace.Name, []string{"shop", "web"}, testGlobal.Ports, checkout.Repo, checkedFile, otherFileByRepo); len(problems) != 0 {
		t.Errorf("checkConfigFile = %v, want no problem", problems)
	}
}

func TestCheckInAWorktreeWithNoFileChecksTheFileOfTheMainCheckout(t *testing.T) {
	_, webDir := shopAndWebForCheck(t)
	webWorktreeDir := filepath.Join(filepath.Dir(webDir), "web-retry")
	runGitInTest(t, webDir, "worktree", "add", "-q", "-b", "retry", webWorktreeDir)
	runGitInTest(t, webWorktreeDir, "rm", "-q", "ilot.yaml")
	currentWorkspace, checkout, err := workspace.Find(webWorktreeDir, t.TempDir(), testGlobal, workspace.NewGit())
	if err != nil {
		t.Fatal(err)
	}

	checkedFile, _, err := configFilesToCheck(currentWorkspace, checkout, "")

	if err != nil || checkedFile != filepath.Join(webDir, "ilot.yaml") {
		t.Fatalf("configFilesToCheck = %q, %v; want the file of the main checkout of web", checkedFile, err)
	}
}

func TestCheckOfARequestedFileKeepsThatFile(t *testing.T) {
	shopDir, _ := shopAndWebForCheck(t)
	draftFile := filepath.Join(shopDir, "draft.yaml")
	if err := os.WriteFile(draftFile, []byte("apps: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	currentWorkspace, checkout, err := workspace.Find(shopDir, t.TempDir(), testGlobal, workspace.NewGit())
	if err != nil {
		t.Fatal(err)
	}

	checkedFile, otherFileByRepo, err := configFilesToCheck(currentWorkspace, checkout, draftFile)

	if err != nil || checkedFile != draftFile || len(otherFileByRepo) != 1 {
		t.Fatalf("configFilesToCheck = %q, %v, %v; want the draft with the file of web", checkedFile, otherFileByRepo, err)
	}
	problems, _ := checkConfigFile(currentWorkspace.Name, []string{"shop", "web"}, testGlobal.Ports, checkout.Repo, checkedFile, otherFileByRepo)
	if joined := errors.Join(problems...); joined == nil || !strings.Contains(joined.Error(), `"api"`) {
		t.Errorf("checkConfigFile = %v, want the port template of web that lost its app", joined)
	}
}

func TestCheckConfigFileNamesTheFileThatHoldsTheProblem(t *testing.T) {
	platformFile := writeConfigFile(t, "apps:\n  gateway: {run: gw, ports: [http]}\n")
	portalFile := writeConfigFile(t, "apps:\n  portal-api: {run: 'api --port={{port \"gateway\" \"htp\"}}'}\n")

	problems, _ := checkConfigFile("platform", []string{"platform", "portal"}, testGlobal.Ports, "platform", platformFile, map[string]string{"portal": portalFile})

	joined := errors.Join(problems...)
	if joined == nil || !strings.Contains(joined.Error(), portalFile) || strings.Contains(joined.Error(), platformFile) {
		t.Fatalf("checkConfigFile = %v, want the problem on %s only", joined, portalFile)
	}
}

func TestCheckConfigFileStillRendersWhenAValidationRuleFails(t *testing.T) {
	configFile := writeConfigFile(t, "apps:\n"+
		"  first: {run: one, ports: [http], route: no-such-port}\n"+
		"  second: {run: 'two --port={{port \"ghost\" \"http\"}}', ports: [http], route: http}\n")

	problems, _ := checkConfigFile("platform", []string{"platform"}, testGlobal.Ports, "platform", configFile, nil)

	joined := errors.Join(problems...)
	if joined == nil || !strings.Contains(joined.Error(), "no-such-port") || !strings.Contains(joined.Error(), "ghost") {
		t.Fatalf("checkConfigFile = %v, want both the route without a port and the template typo", joined)
	}
}
