package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func homeWithPersonalFile(t *testing.T, content string) string {
	t.Helper()
	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, ".config", "ilot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeDir, PersonalSettingsPath), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return homeDir
}

func platformRepo(t *testing.T) string {
	t.Helper()
	return newRepoAt(t, filepath.Join(canonicalTempDir(t), "platform"), map[string]string{PlatformConfigPath: "domain: dev.example.net\napps:\n  standalone: {run: standalone}\n"})
}

func workspaceWithPlatform(t *testing.T, platformPath string) Workspace {
	t.Helper()
	workspace, err := Load(homeWithPersonalFile(t, "repos:\n  platform: {path: "+platformPath+"}\n"), NewGit())
	if err != nil {
		t.Fatal(err)
	}
	return workspace
}

func TestCheckoutsForStackPicksTheStackWorktreeElseTheMainCheckout(t *testing.T) {
	platformDir := newRepo(t, "platform")
	portalDir := newRepo(t, "portal")
	platformWorktreeDir := addWorktree(t, platformDir, "example-feature", "PROJ-1234-example-feature")
	addWorktree(t, portalDir, "other-ticket", "TASK-99-other")

	checkoutByRepo, err := workspaceOfRepos(map[string]string{"platform": platformDir, "portal": portalDir}).CheckoutsForStack("proj-1234")

	if err != nil {
		t.Fatalf("CheckoutsForStack: %v", err)
	}
	if got := checkoutByRepo["platform"]; got.Dir != platformWorktreeDir || got.Branch != "PROJ-1234-example-feature" || got.IsMain {
		t.Errorf("platform checkout = %+v", got)
	}
	if got := checkoutByRepo["portal"]; got.Dir != portalDir || !got.IsMain {
		t.Errorf("portal checkout = %+v, want its main checkout", got)
	}
}

func TestCheckoutsForStackGivesMainCheckoutsToMasterAndDevbox(t *testing.T) {
	platformDir := newRepo(t, "platform")
	addWorktree(t, platformDir, "master", "PROJ-1-x")
	for _, stackName := range []string{"master", "devbox"} {
		checkoutByRepo, err := workspaceOfRepos(map[string]string{"platform": platformDir}).CheckoutsForStack(stackName)
		if err != nil {
			t.Fatalf("CheckoutsForStack(%s): %v", stackName, err)
		}
		if got := checkoutByRepo["platform"]; got.Dir != platformDir {
			t.Errorf("CheckoutsForStack(%s) platform = %+v, want the main checkout", stackName, got)
		}
	}
}

func TestCheckoutsForStackRefusesTwoWorktreesOfOneRepoForATicket(t *testing.T) {
	portalDir := newRepo(t, "portal")
	firstWorktreeDir := addWorktree(t, portalDir, "task-2006-first", "TASK-2006-first")
	secondWorktreeDir := addWorktree(t, portalDir, "task-2006-second", "TASK-2006-second")

	_, err := workspaceOfRepos(map[string]string{"portal": portalDir}).CheckoutsForStack("task-2006")

	if err == nil || !IsSeveralWorktrees(err) || !strings.Contains(err.Error(), firstWorktreeDir) || !strings.Contains(err.Error(), secondWorktreeDir) {
		t.Fatalf("err = %v, want an error naming both worktrees", err)
	}
}

func TestCheckoutsForStackSkipsARepoThatIsNotCloned(t *testing.T) {
	platformDir := newRepo(t, "platform")
	workspace := workspaceOfRepos(map[string]string{"platform": platformDir, "portal": filepath.Join(t.TempDir(), "missing")})

	checkoutByRepo, err := workspace.CheckoutsForStack("master")

	if err != nil {
		t.Fatalf("CheckoutsForStack: %v", err)
	}
	if _, isPresent := checkoutByRepo["portal"]; isPresent || len(checkoutByRepo) != 1 {
		t.Errorf("checkouts = %+v, want only platform", checkoutByRepo)
	}
	if notCloned := workspace.ReposNotCloned(); len(notCloned) != 1 || notCloned["portal"] == "" {
		t.Errorf("ReposNotCloned = %v, want portal", notCloned)
	}
}

func TestRepoOfFindsTheRepoOfAWorktree(t *testing.T) {
	platformDir := newRepo(t, "platform")
	portalDir := newRepo(t, "portal")
	portalWorktreeDir := addWorktree(t, portalDir, "task-1", "TASK-1-x")
	workspace := workspaceOfRepos(map[string]string{"platform": platformDir, "portal": portalDir})
	located, err := workspace.Locate(portalWorktreeDir)
	if err != nil {
		t.Fatal(err)
	}

	repoName, err := workspace.RepoOf(located)

	if err != nil || repoName != "portal" {
		t.Errorf("RepoOf = %q, %v; want portal", repoName, err)
	}
}

func TestRepoOfNamesThePersonalFileKeyForAnUnknownCheckout(t *testing.T) {
	workspace := workspaceOfRepos(map[string]string{"platform": newRepo(t, "platform")})
	located, err := workspace.Locate(newRepo(t, "acme-portal"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = workspace.RepoOf(located)

	if err == nil || !strings.Contains(err.Error(), "repos.") || !strings.Contains(err.Error(), "env.yaml") {
		t.Fatalf("err = %v, want an error naming repos.<name>.path in env.yaml", err)
	}
}

func TestARepoCheckoutWithoutItsConfigFileDeclaresNoApps(t *testing.T) {
	portalDir := newRepoAt(t, filepath.Join(canonicalTempDir(t), "portal"), map[string]string{"README.md": "no ilot.yaml on this branch\n"})
	platformDir := newRepoAt(t, filepath.Join(canonicalTempDir(t), "platform"), map[string]string{
		PlatformConfigPath: "domain: dev.example.net\nrepos:\n  portal: {path: /nowhere, config: ilot.yaml}\napps:\n  standalone: {run: standalone}\n",
	})
	workspace, err := Load(homeWithPersonalFile(t, "repos:\n  platform: {path: "+platformDir+"}\n  portal: {path: "+portalDir+"}\n"), NewGit())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	config, err := workspace.ConfigForStack("master")

	if err != nil {
		t.Fatalf("ConfigForStack: %v", err)
	}
	if _, isKnown := config.Apps["standalone"]; !isKnown {
		t.Errorf("apps = %v, want platform's apps", config.Apps)
	}
}

func TestPlatformWithoutItsConfigNamesThePersonalSetting(t *testing.T) {
	_, err := Load(homeWithPersonalFile(t, "repos:\n  platform: {path: "+t.TempDir()+"}\n"), NewGit())

	if err == nil || !strings.Contains(err.Error(), "repos.platform.path") {
		t.Fatalf("Load = %v, want an error naming repos.platform.path", err)
	}
}

func TestAPersonalRepoWithPathAndConfigJoinsTheWorkspace(t *testing.T) {
	toolDir := newRepoAt(t, filepath.Join(canonicalTempDir(t), "mytool"), map[string]string{"ilot.yaml": "apps:\n  mytool-daemon: {run: mytool daemon}\n"})
	platformDir := platformRepo(t)
	workspace, err := Load(homeWithPersonalFile(t, "repos:\n  platform: {path: "+platformDir+"}\n  mytool: {path: "+toolDir+", config: ilot.yaml}\n  pathonly: {path: "+t.TempDir()+"}\n"), NewGit())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, isKnown := workspace.RepoPaths["pathonly"]; isKnown {
		t.Errorf("a personal repo without a config must stay unknown: %v", workspace.RepoPaths)
	}

	config, err := workspace.ConfigForStack("master")

	if err != nil {
		t.Fatalf("ConfigForStack: %v", err)
	}
	for _, appName := range []string{"mytool-daemon", "standalone"} {
		if _, isKnown := config.Apps[appName]; !isKnown {
			t.Errorf("apps = %v, want %s", config.Apps, appName)
		}
	}
}

func TestCrossStackCommandsUseTheMasterStackFromAnyFolder(t *testing.T) {
	platformDir := platformRepo(t)
	var notes bytes.Buffer

	stackName, worktree, err := workspaceWithPlatform(t, platformDir).StackOf(t.TempDir(), true, &notes)

	if err != nil || stackName != "master" || worktree.Dir != platformDir {
		t.Fatalf("StackOf = %q, %+v, %v; want the master stack of %s", stackName, worktree, err, platformDir)
	}
	if notes.Len() != 0 {
		t.Errorf("notes = %q, want none for a command that works across stacks", notes.String())
	}
}

func TestAFolderOutsideACheckoutUsesMasterAndSaysSo(t *testing.T) {
	var notes bytes.Buffer

	stackName, _, err := workspaceWithPlatform(t, platformRepo(t)).StackOf(t.TempDir(), false, &notes)

	if err != nil || stackName != "master" {
		t.Fatalf("StackOf = %q, %v; want the master stack", stackName, err)
	}
	if !strings.Contains(notes.String(), "not in a checkout") {
		t.Errorf("notes = %q, want the master stack note", notes.String())
	}
}

func TestAFolderInAnUnknownRepoKeepsTheHint(t *testing.T) {
	var notes bytes.Buffer

	_, _, err := workspaceWithPlatform(t, platformRepo(t)).StackOf(newRepo(t, "not-listed"), false, &notes)

	if err == nil || !strings.Contains(err.Error(), "repos.") {
		t.Fatalf("StackOf = %v, want the error that names repos.<name>.path", err)
	}
}

func TestALinkedWorktreeGetsTheStackOfItsTicket(t *testing.T) {
	platformDir := platformRepo(t)
	linkedWorktreeDir := addWorktree(t, platformDir, "retry", "PROJ-1234-retry")
	var notes bytes.Buffer

	stackName, worktree, err := workspaceWithPlatform(t, platformDir).StackOf(linkedWorktreeDir, false, &notes)

	if want := (Worktree{Repo: "platform", Dir: linkedWorktreeDir, Branch: "PROJ-1234-retry", IsMain: false, MainDir: platformDir}); err != nil || stackName != "proj-1234" || worktree != want {
		t.Fatalf("StackOf = %q, %+v, %v; want proj-1234 and %+v", stackName, worktree, err, want)
	}
}

func TestCrossStackCommandsIgnoreALinkedPlatformPath(t *testing.T) {
	platformDir := platformRepo(t)
	linkedWorktreeDir := addWorktree(t, platformDir, "proj-1234-work", "PROJ-1234-work")
	var notes bytes.Buffer

	stackName, worktree, err := workspaceWithPlatform(t, linkedWorktreeDir).StackOf(t.TempDir(), true, &notes)

	if err != nil || stackName != "master" || worktree.Dir != platformDir {
		t.Fatalf("StackOf = %q, %+v, %v; want master from the main checkout %s", stackName, worktree, err, platformDir)
	}
}

func TestAHomeFolderThatIsACheckoutUsesMasterAndSaysSo(t *testing.T) {
	platformDir := platformRepo(t)
	homeDir := homeWithPersonalFile(t, "repos:\n  platform: {path: "+platformDir+"}\n")
	gitInTest(t, homeDir, "init", "-q", "-b", "main")
	gitInTest(t, homeDir, "commit", "-q", "--allow-empty", "-m", "dotfiles")
	linkedHomeDir := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(homeDir, linkedHomeDir); err != nil {
		t.Fatal(err)
	}
	for _, reachedHomeDir := range []string{homeDir, linkedHomeDir} {
		workspace, err := Load(reachedHomeDir, NewGit())
		if err != nil {
			t.Fatal(err)
		}
		var notes bytes.Buffer

		stackName, worktree, err := workspace.StackOf(reachedHomeDir, false, &notes)

		if err != nil || stackName != "master" || worktree.Dir != platformDir {
			t.Fatalf("StackOf(%s) = %q, %+v, %v; want the master stack of %s", reachedHomeDir, stackName, worktree, err, platformDir)
		}
		if !strings.Contains(notes.String(), "home folder") {
			t.Errorf("notes = %q, want the home folder note", notes.String())
		}
	}
}

func TestARepoPathThroughASymbolicLinkIsTheSameRepo(t *testing.T) {
	platformDir := platformRepo(t)
	linkedPlatformDir := filepath.Join(canonicalTempDir(t), "platform-link")
	if err := os.Symlink(platformDir, linkedPlatformDir); err != nil {
		t.Fatal(err)
	}
	workspace := workspaceWithPlatform(t, linkedPlatformDir)
	var notes bytes.Buffer

	if got := workspace.RepoPaths["platform"]; got != platformDir {
		t.Errorf("RepoPaths[platform] = %q, want the canonical %q", got, platformDir)
	}
	stackName, worktree, err := workspace.StackOf(linkedPlatformDir, false, &notes)

	if err != nil || stackName != "master" || worktree.Dir != platformDir || worktree.MainDir != platformDir {
		t.Fatalf("StackOf through a link = %q, %+v, %v; want the master stack of %s", stackName, worktree, err, platformDir)
	}
	if notes.Len() != 0 {
		t.Errorf("notes = %q, want none inside a known repo", notes.String())
	}
}
