package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/config"
)

const webFile = "member_of: ../shop\napps:\n  front: {run: front}\n"

// shopAndWeb makes a workspace of two repos in one folder: shop, the root, and web, a member.
func shopAndWeb(t *testing.T, shopFile, webFile string) (shopDir, webDir string) {
	t.Helper()
	codeDir := canonicalTempDir(t)
	shopDir = newRepoAt(t, filepath.Join(codeDir, "shop"), map[string]string{"ilot.yaml": shopFile})
	webDir = newRepoAt(t, filepath.Join(codeDir, "web"), map[string]string{"ilot.yaml": webFile})
	return shopDir, webDir
}

func defaultGlobal(t *testing.T) config.Global {
	t.Helper()
	global, err := config.LoadGlobal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return global
}

func find(t *testing.T, dir string) (Workspace, Worktree) {
	t.Helper()
	workspace, worktree, err := Find(dir, t.TempDir(), defaultGlobal(t), NewGit())
	if err != nil {
		t.Fatalf("Find(%s): %v", dir, err)
	}
	return workspace, worktree
}

func TestFindFromTheRootRepo(t *testing.T) {
	shopDir, webDir := shopAndWeb(t, "repos:\n  web: ../web\napps:\n  api: {run: api}\n", webFile)

	workspace, worktree := find(t, shopDir)

	if workspace.Name != "shop" || workspace.RootDir != shopDir || len(workspace.RepoPaths) != 2 || workspace.RepoPaths["shop"] != shopDir || workspace.RepoPaths["web"] != webDir {
		t.Errorf("workspace = %+v, want shop with the repos shop and web", workspace)
	}
	if want := (Worktree{Repo: "shop", Dir: shopDir, Branch: "main", IsMain: true, MainDir: shopDir}); worktree != want {
		t.Errorf("worktree = %+v, want %+v", worktree, want)
	}
}

func TestFindInARepoWithNoCommit(t *testing.T) {
	shopDir := filepath.Join(canonicalTempDir(t), "shop")
	if err := os.MkdirAll(shopDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, shopDir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(shopDir, "ilot.yaml"), []byte("apps:\n  api: {run: api}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, worktree := find(t, shopDir)

	if want := (Worktree{Repo: "shop", Dir: shopDir, Branch: "main", IsMain: true, MainDir: shopDir}); worktree != want {
		t.Errorf("worktree = %+v, want %+v", worktree, want)
	}
}

func TestFindFromAMemberRepoFollowsMemberOf(t *testing.T) {
	shopDir, webDir := shopAndWeb(t, "name: store\nrepos:\n  web: ../web\n", webFile)
	if err := os.MkdirAll(filepath.Join(webDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	workspace, worktree := find(t, filepath.Join(webDir, "src"))

	if workspace.Name != "store" || workspace.RootDir != shopDir || workspace.RepoPaths["store"] != shopDir || workspace.RepoPaths["web"] != webDir {
		t.Errorf("workspace = %+v, want store with the root %s", workspace, shopDir)
	}
	if worktree.Repo != "web" || worktree.Dir != webDir || !worktree.IsMain {
		t.Errorf("worktree = %+v, want the main checkout of web", worktree)
	}
}

func TestFindFromAWorktreeOfAMemberReadsTheFileOfTheMainCheckout(t *testing.T) {
	shopDir, webDir := shopAndWeb(t, "repos:\n  web: ../web\n", webFile)
	webWorktreeDir := addWorktree(t, webDir, "web-retry", "retry-jobs")
	gitInTest(t, webWorktreeDir, "rm", "-q", "ilot.yaml")

	workspace, worktree := find(t, webWorktreeDir)

	if workspace.Name != "shop" || workspace.RootDir != shopDir {
		t.Errorf("workspace = %+v, want shop", workspace)
	}
	if want := (Worktree{Repo: "web", Dir: webWorktreeDir, Branch: "retry-jobs", IsMain: false, MainDir: webDir}); worktree != want {
		t.Errorf("worktree = %+v, want %+v", worktree, want)
	}
}

func TestAMemberThatTheRootDoesNotListIsAnError(t *testing.T) {
	shopDir, webDir := shopAndWeb(t, "apps:\n  api: {run: api}\n", webFile)

	_, _, err := Find(webDir, t.TempDir(), defaultGlobal(t), NewGit())

	for _, wantPart := range []string{filepath.Join(webDir, "ilot.yaml"), filepath.Join(shopDir, "ilot.yaml"), "member_of: ../shop", "web: ../web"} {
		if err == nil || !strings.Contains(err.Error(), wantPart) {
			t.Errorf("Find = %v, want an error with %q", err, wantPart)
		}
	}
}

func TestAWrongMemberOfIsAnError(t *testing.T) {
	testCases := []struct {
		name            string
		memberOf        string
		rootFile        string
		wantErrorSubstr string
	}{
		{name: "no folder", memberOf: "../nowhere", wantErrorSubstr: "is not a git checkout"},
		{name: "a root with no file", memberOf: "../shop", wantErrorSubstr: "does not exist"},
		{name: "a root that is a member", memberOf: "../shop", rootFile: "member_of: ../elsewhere\n", wantErrorSubstr: "a root repo has none"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			codeDir := canonicalTempDir(t)
			rootFiles := map[string]string{}
			if testCase.rootFile != "" {
				rootFiles["ilot.yaml"] = testCase.rootFile
			}
			newRepoAt(t, filepath.Join(codeDir, "shop"), rootFiles)
			webDir := newRepoAt(t, filepath.Join(codeDir, "web"), map[string]string{"ilot.yaml": "member_of: " + testCase.memberOf + "\n"})

			_, _, err := Find(webDir, t.TempDir(), defaultGlobal(t), NewGit())

			if err == nil || !strings.Contains(err.Error(), testCase.wantErrorSubstr) || !strings.Contains(err.Error(), filepath.Join(webDir, "ilot.yaml")) {
				t.Errorf("Find = %v, want an error with %q that names the file of the member", err, testCase.wantErrorSubstr)
			}
		})
	}
}

func TestARootThatListsARepoWhichDoesNotNameItIsAnError(t *testing.T) {
	testCases := []struct {
		name            string
		webFile         string
		wantErrorSubstr string
	}{
		{name: "no member_of", webFile: "apps:\n  front: {run: front}\n", wantErrorSubstr: "add member_of: ../shop"},
		{name: "another root", webFile: "member_of: ../blog\n", wantErrorSubstr: "names another root repo"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			shopDir, webDir := shopAndWeb(t, "repos:\n  web: ../web\n", testCase.webFile)
			newRepoAt(t, filepath.Join(filepath.Dir(shopDir), "blog"), map[string]string{"ilot.yaml": "apps: {}\n"})

			_, _, err := Find(shopDir, t.TempDir(), defaultGlobal(t), NewGit())

			for _, wantPart := range []string{testCase.wantErrorSubstr, filepath.Join(shopDir, "ilot.yaml"), filepath.Join(webDir, "ilot.yaml")} {
				if err == nil || !strings.Contains(err.Error(), wantPart) {
					t.Errorf("Find = %v, want an error with %q", err, wantPart)
				}
			}
		})
	}
}

func TestAMemberWithNoFileDeclaresNoApp(t *testing.T) {
	codeDir := canonicalTempDir(t)
	shopDir := newRepoAt(t, filepath.Join(codeDir, "shop"), map[string]string{"ilot.yaml": "repos:\n  schemas: ../schemas\n  later: ../not-cloned\napps:\n  api: {run: api}\n"})
	schemasDir := newRepoAt(t, filepath.Join(codeDir, "schemas"), map[string]string{"README.md": "no apps here\n"})
	workspace, _ := find(t, shopDir)

	config, err := workspace.ConfigForStack("base")
	if err != nil || len(config.Apps) != 1 {
		t.Errorf("ConfigForStack = %+v, %v; want the app of shop only", config.Apps, err)
	}
	checkoutByRepo, err := workspace.CheckoutsForStack("base")
	if err != nil || len(checkoutByRepo) != 2 || checkoutByRepo["schemas"].Dir != schemasDir {
		t.Errorf("CheckoutsForStack = %+v, %v; want shop and schemas", checkoutByRepo, err)
	}
	if notCloned := workspace.ReposNotCloned(); len(notCloned) != 1 || notCloned["later"] != filepath.Join(codeDir, "not-cloned") {
		t.Errorf("ReposNotCloned = %v, want later", notCloned)
	}
}

func TestFindOutsideAWorkspaceSaysWhatItLooksFor(t *testing.T) {
	_, _, err := Find(t.TempDir(), t.TempDir(), defaultGlobal(t), NewGit())
	if err == nil || !strings.Contains(err.Error(), "not inside a git checkout") || !strings.Contains(err.Error(), "ilot.yaml") {
		t.Errorf("Find outside a repo = %v, want an error that names the git checkout and ilot.yaml", err)
	}

	repoDir := newRepoAt(t, filepath.Join(canonicalTempDir(t), "platform"), map[string]string{"devbox/ilot.yaml": "apps: {}\n"})
	_, _, err = Find(repoDir, t.TempDir(), defaultGlobal(t), NewGit())
	if err == nil || !strings.Contains(err.Error(), "has no ilot.yaml") || !strings.Contains(err.Error(), "devbox/ilot.yaml is the old place") {
		t.Errorf("Find in a repo with the old layout = %v, want an error that says where the file goes", err)
	}
}

func TestTheNameOfTheWorkspace(t *testing.T) {
	testCases := []struct {
		folder          string
		rootFile        string
		wantName        string
		wantErrorSubstr string
	}{
		{folder: "shop", rootFile: "apps: {}\n", wantName: "shop"},
		{folder: "My_Shop.v2", rootFile: "apps: {}\n", wantName: "my-shop-v2"},
		{folder: "shop", rootFile: "name: store\n", wantName: "store"},
		{folder: "shop", rootFile: "name: My Store\n", wantErrorSubstr: "lowercase letters, digits and dashes"},
		{folder: "___", rootFile: "apps: {}\n", wantErrorSubstr: "set name"},
		{folder: "shop", rootFile: "repos:\n  shop: ../other\n", wantErrorSubstr: "has the name of the workspace"},
	}
	for _, testCase := range testCases {
		rootDir := newRepoAt(t, filepath.Join(canonicalTempDir(t), testCase.folder), map[string]string{"ilot.yaml": testCase.rootFile})

		workspace, _, err := Find(rootDir, t.TempDir(), defaultGlobal(t), NewGit())

		switch {
		case testCase.wantErrorSubstr != "":
			if err == nil || !strings.Contains(err.Error(), testCase.wantErrorSubstr) {
				t.Errorf("Find in %s with %q = %v, want an error with %q", testCase.folder, testCase.rootFile, err, testCase.wantErrorSubstr)
			}
		case err != nil || workspace.Name != testCase.wantName:
			t.Errorf("Find in %s with %q = %q, %v; want the name %q", testCase.folder, testCase.rootFile, workspace.Name, err, testCase.wantName)
		}
	}
}

func TestTheStackPatternComesFromTheRootRepo(t *testing.T) {
	shopDir, webDir := shopAndWeb(t, "repos:\n  web: ../web\nstack:\n  pattern: '[A-Z]+-[0-9]+'\n", webFile)
	webWorktreeDir := addWorktree(t, webDir, "web-retry", "feature/PROJ-12-retry")

	workspace, worktree := find(t, webWorktreeDir)

	if stackName, err := workspace.DefaultStackName(worktree); err != nil || stackName != "proj-12" {
		t.Errorf("DefaultStackName = %q, %v; want proj-12, from the pattern of %s", stackName, err, shopDir)
	}
}

func TestAWrongStackPatternNamesTheFile(t *testing.T) {
	shopDir := newRepoAt(t, filepath.Join(canonicalTempDir(t), "shop"), map[string]string{"ilot.yaml": "stack:\n  pattern: '[A-Z'\n"})

	_, _, err := Find(shopDir, t.TempDir(), defaultGlobal(t), NewGit())

	if err == nil || !strings.Contains(err.Error(), "stack.pattern") || !strings.Contains(err.Error(), filepath.Join(shopDir, "ilot.yaml")) {
		t.Errorf("Find = %v, want an error that names stack.pattern and the file", err)
	}
}

func TestTheGlobalFileGivesAnotherPathToARepo(t *testing.T) {
	shopDir := newRepoAt(t, filepath.Join(canonicalTempDir(t), "shop"), map[string]string{"ilot.yaml": "repos:\n  web: ../web\n"})
	homeDir := canonicalTempDir(t)
	webDir := newRepoAt(t, filepath.Join(homeDir, "elsewhere", "web"), map[string]string{"ilot.yaml": "member_of: " + shopDir + "\n"})
	global := config.Global{Ports: config.PortRange{First: 20000, Last: 29999}, Workspaces: map[string]config.WorkspaceSettings{
		"shop":  {Repos: map[string]string{"web": "~/elsewhere/web"}, Env: map[string]string{"LOG_LEVEL": "debug"}},
		"other": {Repos: map[string]string{"web": "/nowhere"}},
	}}

	for _, dir := range []string{shopDir, webDir} {
		workspace, _, err := Find(dir, homeDir, global, NewGit())
		if err != nil || workspace.RepoPaths["web"] != webDir || workspace.Settings.Env["LOG_LEVEL"] != "debug" {
			t.Errorf("Find(%s) = %+v, %v; want web at %s and the settings of shop", dir, workspace, err, webDir)
		}
	}
}

func TestAPathInTheGlobalFileForARepoThatTheRootDoesNotListIsAnError(t *testing.T) {
	shopDir := newRepoAt(t, filepath.Join(canonicalTempDir(t), "shop"), map[string]string{"ilot.yaml": "repos:\n  web: ../web\n"})
	global := config.Global{Workspaces: map[string]config.WorkspaceSettings{"shop": {Repos: map[string]string{"wbe": "~/elsewhere/web"}}}}

	_, _, err := Find(shopDir, t.TempDir(), global, NewGit())

	if err == nil || !strings.Contains(err.Error(), "workspaces.shop.repos.wbe") || !strings.Contains(err.Error(), filepath.Join(shopDir, "ilot.yaml")) {
		t.Errorf("Find = %v, want an error that names the key and the file of the root", err)
	}
}

func TestARepoReachedThroughALinkIsTheSameRepo(t *testing.T) {
	shopDir, webDir := shopAndWeb(t, "repos:\n  web: ../web-link\n", "member_of: ../shop-link\n")
	for linkName, target := range map[string]string{"shop-link": shopDir, "web-link": webDir} {
		if err := os.Symlink(target, filepath.Join(filepath.Dir(shopDir), linkName)); err != nil {
			t.Fatal(err)
		}
	}

	for _, dir := range []string{shopDir, webDir, filepath.Join(filepath.Dir(shopDir), "shop-link"), filepath.Join(filepath.Dir(shopDir), "web-link")} {
		workspace, _, err := Find(dir, t.TempDir(), defaultGlobal(t), NewGit())
		if err != nil || workspace.RootDir != shopDir || workspace.RepoPaths["web"] != webDir {
			t.Errorf("Find(%s) = %+v, %v; want the root %s and web at %s", dir, workspace, err, shopDir, webDir)
		}
	}
}

func TestAStackReadsTheFileOfItsOwnCheckoutElseTheOneOfTheMainCheckout(t *testing.T) {
	shopDir, webDir := shopAndWeb(t, "repos:\n  web: ../web\nstack:\n  pattern: '[A-Z]+-[0-9]+'\napps:\n  api: {run: api}\n", webFile)
	shopWorktreeDir := addWorktree(t, shopDir, "shop-retry", "PROJ-1-retry")
	if err := os.WriteFile(filepath.Join(shopWorktreeDir, "ilot.yaml"), []byte("apps:\n  api: {run: api --retry}\n  jobs: {run: jobs}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	webWorktreeDir := addWorktree(t, webDir, "web-retry", "PROJ-1-retry")
	gitInTest(t, webWorktreeDir, "rm", "-q", "ilot.yaml")
	workspace, _ := find(t, shopWorktreeDir)
	workspace.Bindings = Bindings{"proj-1": {"shop": shopWorktreeDir, "web": webWorktreeDir}}

	ticketConfig, err := workspace.ConfigForStack("proj-1")
	if err != nil || ticketConfig.Apps["api"].Run != "api --retry" || ticketConfig.Apps["jobs"].Repo != "shop" || ticketConfig.Apps["front"].Repo != "web" {
		t.Errorf("config of proj-1 = %+v, %v; want the apps of the shop worktree, and front from the main checkout of web", ticketConfig.Apps, err)
	}
	baseConfig, err := workspace.ConfigForStack("base")
	if err != nil || baseConfig.Apps["api"].Run != "api" || len(baseConfig.Apps) != 2 {
		t.Errorf("config of base = %+v, %v; want the apps of the main checkouts", baseConfig.Apps, err)
	}
}

func TestCheckoutsForStackGivesTheBoundWorktreeElseTheMainCheckout(t *testing.T) {
	platformDir := newRepo(t, "platform")
	portalDir := newRepo(t, "portal")
	platformWorktreeDir := addWorktree(t, platformDir, "example-feature", "retry-jobs")
	addWorktree(t, portalDir, "same-branch-not-bound", "retry-jobs")
	workspace := workspaceOfRepos(map[string]string{"platform": platformDir, "portal": portalDir})
	workspace.Bindings = Bindings{"retry-jobs": {"platform": platformWorktreeDir}}

	checkoutByRepo, err := workspace.CheckoutsForStack("retry-jobs")

	if err != nil {
		t.Fatalf("CheckoutsForStack: %v", err)
	}
	if got := checkoutByRepo["platform"]; got.Dir != platformWorktreeDir || got.Branch != "retry-jobs" || got.IsMain {
		t.Errorf("platform checkout = %+v, want the bound worktree", got)
	}
	if got := checkoutByRepo["portal"]; got.Dir != portalDir || !got.IsMain {
		t.Errorf("portal checkout = %+v, want its main checkout: only a binding puts a worktree in a stack", got)
	}
}

func TestCheckoutsForStackGivesTheMainCheckoutsToTheBaseStack(t *testing.T) {
	platformDir := newRepo(t, "platform")
	addWorktree(t, platformDir, "elsewhere", "PROJ-1-x")

	checkoutByRepo, err := workspaceOfRepos(map[string]string{"platform": platformDir}).CheckoutsForStack("base")

	if got := checkoutByRepo["platform"]; err != nil || got.Dir != platformDir {
		t.Errorf("CheckoutsForStack(base) platform = %+v, %v; want the main checkout", got, err)
	}
}

func TestCheckoutsForStackGoesBackToTheMainCheckoutWhenTheBoundWorktreeIsDeleted(t *testing.T) {
	platformDir := newRepo(t, "platform")
	deletedWorktreeDir := addWorktree(t, platformDir, "old-attempt", "retry-jobs")
	if err := os.RemoveAll(deletedWorktreeDir); err != nil {
		t.Fatal(err)
	}
	workspace := workspaceOfRepos(map[string]string{"platform": platformDir})
	workspace.Bindings = Bindings{"retry-jobs": {"platform": deletedWorktreeDir}}

	checkoutByRepo, err := workspace.CheckoutsForStack("retry-jobs")

	if got := checkoutByRepo["platform"]; err != nil || got.Dir != platformDir {
		t.Errorf("platform checkout = %+v, %v; want the main checkout", got, err)
	}
}

func TestCheckoutsForStackSkipsARepoThatIsNotCloned(t *testing.T) {
	platformDir := newRepo(t, "platform")
	workspace := workspaceOfRepos(map[string]string{"platform": platformDir, "portal": filepath.Join(t.TempDir(), "missing")})

	checkoutByRepo, err := workspace.CheckoutsForStack("base")

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
