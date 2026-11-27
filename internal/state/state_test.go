package state

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func sampleState() State {
	sample := New()
	if err := sample.Claim("shop", "/code/shop"); err != nil {
		panic(err)
	}
	sample.Stack("shop", "base").Checkouts["shop"] = "/code/shop"
	sample.Stack("shop", "base").Apps["api"] = App{Wanted: true, Ports: map[string]int{"http": 20000}, Route: &Route{Port: 20000, HomePath: "", Dir: "/code/shop"}, Container: ""}
	sample.Stack("shop", "retry-jobs").Checkouts["shop"] = "/code/shop-retry"
	sample.Stack("shop", "retry-jobs").Apps["api"] = App{Wanted: false, Ports: map[string]int{"http": 20001}, Route: nil, Container: ""}
	return sample
}

func TestSaveThenLoadKeepsEveryWorkspaceInOneVersionedFile(t *testing.T) {
	store := Store{RootDir: filepath.Join(t.TempDir(), "ilot")}
	if _, err := store.Save(sampleState()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	api := loaded.App("shop", "base", "api")
	if loaded.Version != CurrentVersion || loaded.Workspaces["shop"].Root != "/code/shop" || !api.Wanted || api.Ports["http"] != 20000 || api.Route.Port != 20000 {
		t.Errorf("Load = %+v with api %+v", loaded, api)
	}
	if loaded.Stacks("shop")["retry-jobs"].Checkouts["shop"] != "/code/shop-retry" || loaded.App("shop", "retry-jobs", "api").Wanted {
		t.Errorf("retry-jobs = %+v", loaded.Stacks("shop")["retry-jobs"])
	}
	for path, wantMode := range map[string]os.FileMode{store.RootDir: 0o700, store.File(): 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != wantMode {
			t.Errorf("%s: mode %v, %v; want %o", path, info, err, wantMode)
		}
	}
}

func TestLoadWithNoFileGivesAnEmptyState(t *testing.T) {
	loaded, err := Store{RootDir: t.TempDir()}.Load()
	if err != nil || loaded.Version != CurrentVersion || len(loaded.Workspaces) != 0 {
		t.Errorf("Load = %+v, %v; want an empty state of the current version", loaded, err)
	}
	if app := loaded.App("shop", "base", "api"); app.Wanted || len(app.Ports) != 0 {
		t.Errorf("an unknown app = %+v, want an app that is not wanted", app)
	}
}

func TestLoadRefusesAFileOfAnotherVersion(t *testing.T) {
	store := Store{RootDir: t.TempDir()}
	if err := os.WriteFile(store.File(), []byte(`{"version": 2, "workspaces": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := store.Load()

	if err == nil || !strings.Contains(err.Error(), "version 2") || !strings.Contains(err.Error(), store.File()) {
		t.Errorf("Load = %v, want an error with the file and its version", err)
	}
}

func TestAnUnreadableFileGivesOneErrorThatNamesTheFileAndItsBackup(t *testing.T) {
	store := Store{RootDir: t.TempDir()}
	first := sampleState()
	if _, err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	second := sampleState()
	second.RemoveStack("shop", "retry-jobs")
	if _, err := store.Save(second); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.File(), []byte(`{"version": 1, "workspaces": {"shop"`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := store.Load()

	if err == nil || !strings.Contains(err.Error(), store.File()) || !strings.Contains(err.Error(), store.BackupFile()) {
		t.Fatalf("Load = %v, want an error that names the file and the backup", err)
	}
	backup, err := Store{RootDir: backupAsState(t, store)}.Load()
	if err != nil || backup.Stacks("shop")["retry-jobs"] == nil {
		t.Errorf("the backup = %+v, %v; want the version before the last Save, with retry-jobs", backup, err)
	}
}

func TestASaveOfTheSameStateKeepsTheBackupOfTheLastChange(t *testing.T) {
	store := Store{RootDir: t.TempDir()}
	first := sampleState()
	if _, err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	second := sampleState()
	second.RemoveStack("shop", "retry-jobs")
	if isChanged, err := store.Save(second); err != nil || !isChanged {
		t.Fatalf("Save of a new state = %t, %v; want a write", isChanged, err)
	}

	isChanged, err := store.Save(second)

	if err != nil || isChanged {
		t.Fatalf("Save of the same state = %t, %v; want no write", isChanged, err)
	}
	backup, err := Store{RootDir: backupAsState(t, store)}.Load()
	if err != nil || backup.Stacks("shop")["retry-jobs"] == nil {
		t.Errorf("the backup = %+v, %v; want the version before the last change, with retry-jobs", backup, err)
	}
}

func backupAsState(t *testing.T, store Store) string {
	t.Helper()
	content, err := os.ReadFile(store.BackupFile())
	if err != nil {
		t.Fatal(err)
	}
	restoredDir := t.TempDir()
	if err := os.WriteFile(Store{RootDir: restoredDir}.File(), content, 0o600); err != nil {
		t.Fatal(err)
	}
	return restoredDir
}

func TestLockGivesUpAfterTheTimeoutAndNamesTheHolder(t *testing.T) {
	store := Store{RootDir: t.TempDir(), LockTimeout: 200 * time.Millisecond}
	release, err := store.Lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var announcedHolder string
	store.OnLockWait = func(holder string) { announcedHolder = holder }

	_, err = store.Lock(t.Context())

	wantHolder := "process " + strconv.Itoa(os.Getpid())
	if err == nil || !strings.Contains(err.Error(), wantHolder) || !strings.Contains(err.Error(), "200ms") {
		t.Errorf("the second Lock = %v, want an error with the timeout and %q", err, wantHolder)
	}
	if !strings.Contains(announcedHolder, wantHolder) {
		t.Errorf("OnLockWait got %q, want %q", announcedHolder, wantHolder)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	release, err = store.Lock(t.Context())
	if err != nil {
		t.Fatalf("Lock after the release: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestACancelEndsTheWaitForTheLock(t *testing.T) {
	store := Store{RootDir: t.TempDir()}
	release, err := store.Lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, err = store.Lock(ctx)

	if err == nil || ctx.Err() == nil {
		t.Errorf("Lock with a cancelled context = %v, want the error of the context", err)
	}
}

func TestClaimRefusesASecondWorkspaceOfTheSameName(t *testing.T) {
	shopDir, otherShopDir := t.TempDir(), t.TempDir()
	current := New()
	if err := current.Claim("shop", shopDir); err != nil {
		t.Fatalf("the first claim: %v", err)
	}
	if err := current.Claim("shop", shopDir); err != nil {
		t.Fatalf("the same workspace again: %v", err)
	}

	err := current.Claim("shop", otherShopDir)
	if err == nil || !strings.Contains(err.Error(), "Set name") || !strings.Contains(err.Error(), shopDir) || !strings.Contains(err.Error(), otherShopDir) {
		t.Errorf("a second workspace of the same name = %v, want an error that names both folders and the name key", err)
	}
	if err := current.Claim("blog", otherShopDir); err != nil {
		t.Errorf("a workspace of another name = %v, want none: one state holds every workspace", err)
	}
}

func TestClaimTakesTheNameOfAWorkspaceWhoseFolderIsGone(t *testing.T) {
	current := New()
	if err := current.Claim("shop", filepath.Join(t.TempDir(), "deleted")); err != nil {
		t.Fatal(err)
	}
	newShopDir := t.TempDir()

	if err := current.Claim("shop", newShopDir); err != nil || current.Workspaces["shop"].Root != newShopDir {
		t.Errorf("Claim after the first root was deleted = %v with the root %q, want the new root", err, current.Workspaces["shop"].Root)
	}
}

func TestPortsInUseCoversEveryWorkspaceAndCloneSharesNothing(t *testing.T) {
	current := sampleState()
	current.Stack("blog", "base").Apps["web"] = App{Wanted: true, Ports: map[string]int{"http": 20002}, Route: nil, Container: ""}

	cloned := current.Clone()
	cloned.Stack("shop", "base").Apps["api"].Ports["http"] = 1
	cloned.Stack("shop", "base").Apps["api"].Route.Port = 1

	portsInUse := current.PortsInUse()
	if len(portsInUse) != 3 || !portsInUse[20000] || !portsInUse[20001] || !portsInUse[20002] {
		t.Errorf("PortsInUse = %v, want the ports of both workspaces", portsInUse)
	}
	if current.App("shop", "base", "api").Route.Port != 20000 {
		t.Error("a change of the clone reached the route of the original")
	}
}

func TestStackDirIsUnderTheWorkspace(t *testing.T) {
	if got := (Store{RootDir: "/state"}).StackDir("shop", "retry-jobs"); got != filepath.Join("/state", "workspaces", "shop", "retry-jobs") {
		t.Errorf("StackDir = %q", got)
	}
}
