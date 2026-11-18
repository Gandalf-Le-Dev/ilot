package state

import (
	"os"
	"path/filepath"
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

func TestLoadMapsALegacyWorktreeDir(t *testing.T) {
	store := Store{RootDir: t.TempDir()}
	if err := os.MkdirAll(store.StackDir("proj-1234"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacyState := `{"name": "proj-1234", "worktree_dir": "/worktrees/proj-1234", "apps": {}}`
	if err := os.WriteFile(filepath.Join(store.StackDir("proj-1234"), "state.json"), []byte(legacyState), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load("proj-1234")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.CheckoutDirs["platform"] != "/worktrees/proj-1234" {
		t.Errorf("CheckoutDirs = %v", loaded.CheckoutDirs)
	}
}

func TestSaveKeepsTheStateReadableByItsOwnerOnly(t *testing.T) {
	store := Store{RootDir: t.TempDir()}

	if err := store.Save(StackState{Name: "proj-1234", CheckoutDirs: map[string]string{}, LegacyWorktreeDir: "", HasTemporalNamespace: false, Apps: map[string]AppState{}}); err != nil {
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
