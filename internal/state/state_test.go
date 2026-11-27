package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveThenLoadKeepsPortsAndRunningFlag(t *testing.T) {
	store := Store{RootDir: t.TempDir()}
	saved := StackState{
		Name:         "proj-1234",
		CheckoutDirs: map[string]string{"platform": "/home/user/worktrees/proj-1234", "portal": "/home/user/portal"},
		Apps: map[string]AppState{
			"publicapi": {Ports: map[string]int{"http": 20000, "monitoring": 20001}, Running: true},
		},
	}
	if err := store.Save(saved); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load("proj-1234")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	publicapi := loaded.Apps["publicapi"]
	if loaded.CheckoutDirs["portal"] != "/home/user/portal" || publicapi.Ports["monitoring"] != 20001 || !publicapi.Running {
		t.Errorf("Load = %+v", loaded)
	}
}

func TestLoadOfAnUnknownStackGivesAnEmptyState(t *testing.T) {
	store := Store{RootDir: t.TempDir()}
	loaded, err := store.Load("new-stack")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Name != "new-stack" || len(loaded.Apps) != 0 {
		t.Errorf("Load = %+v", loaded)
	}
}

func TestAllListsEveryStackByName(t *testing.T) {
	store := Store{RootDir: t.TempDir()}
	for _, stackState := range []StackState{
		{Name: "master", Apps: map[string]AppState{"gateway": {Ports: map[string]int{"http": 20000}}}},
		{Name: "proj-1234", Apps: map[string]AppState{"gateway": {Ports: map[string]int{"http": 20001}}}},
	} {
		if err := store.Save(stackState); err != nil {
			t.Fatal(err)
		}
	}

	allStacks, err := store.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(allStacks) != 2 || allStacks[0].Name != "master" || allStacks[1].Name != "proj-1234" {
		t.Errorf("All = %+v", allStacks)
	}
}

func TestStackDirIsUnderTheRoot(t *testing.T) {
	store := Store{RootDir: "/state"}
	if got := store.StackDir("proj-1234"); got != filepath.Join("/state", "stacks", "proj-1234") {
		t.Errorf("StackDir = %q", got)
	}
}

func TestLockCanBeTakenAgainAfterRelease(t *testing.T) {
	store := Store{RootDir: t.TempDir()}
	for range 2 {
		release, err := store.Lock()
		if err != nil {
			t.Fatalf("Lock: %v", err)
		}
		if err := release(); err != nil {
			t.Fatalf("release: %v", err)
		}
	}
}

func TestSaveKeepsTheStateReadableByItsOwnerOnly(t *testing.T) {
	store := Store{RootDir: t.TempDir()}

	if err := store.Save(StackState{Name: "proj-1234", CheckoutDirs: map[string]string{}, Apps: map[string]AppState{}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	for path, wantMode := range map[string]os.FileMode{store.StackDir("proj-1234"): 0o700, store.stateFile("proj-1234"): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != wantMode {
			t.Errorf("%s mode = %o, want %o", path, info.Mode().Perm(), wantMode)
		}
	}
}

func TestClaimKeepsTheStateForOneWorkspace(t *testing.T) {
	store := Store{RootDir: filepath.Join(t.TempDir(), "ilot")}
	shopDir, otherShopDir, blogDir := t.TempDir(), t.TempDir(), t.TempDir()
	if err := store.Claim("shop", shopDir); err != nil {
		t.Fatalf("the first claim: %v", err)
	}
	if err := store.Claim("shop", shopDir); err != nil {
		t.Fatalf("the same workspace again: %v", err)
	}

	err := store.Claim("shop", otherShopDir)
	if err == nil || !strings.Contains(err.Error(), "Set name") || !strings.Contains(err.Error(), shopDir) || !strings.Contains(err.Error(), otherShopDir) {
		t.Errorf("a second workspace of the same name = %v, want an error that names both folders and the name key", err)
	}
	err = store.Claim("blog", blogDir)
	if err == nil || !strings.Contains(err.Error(), `"shop"`) || !strings.Contains(err.Error(), "XDG_STATE_HOME") {
		t.Errorf("another workspace = %v, want an error that names the owner and the way out", err)
	}
	if err := store.Claim("store", shopDir); err != nil {
		t.Errorf("the same root with a new name = %v, want the new name recorded", err)
	}
}

func TestClaimTakesTheStateOfAWorkspaceWhoseFolderIsGone(t *testing.T) {
	store := Store{RootDir: t.TempDir()}
	if err := store.Claim("shop", filepath.Join(t.TempDir(), "deleted")); err != nil {
		t.Fatal(err)
	}

	if err := store.Claim("blog", t.TempDir()); err != nil {
		t.Errorf("Claim after the owner was deleted = %v, want none", err)
	}
}
