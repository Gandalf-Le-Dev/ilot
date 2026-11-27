package logs

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const userOnlyPerm = 0o600

func TestRotatingFileKeepsTwoOldFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "app.log")
	rotating, err := OpenRotating(path, 10, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []string{"aaaaaa", "bbbbbb", "cccccc", "dddddd"} {
		if _, err := rotating.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := rotating.Close(); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{path: "dddddd", path + ".1": "cccccc", path + ".2": "bbbbbb"} {
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, []byte(want)) {
			t.Errorf("%s holds %q, want %q", filepath.Base(name), content, want)
		}
	}
	if _, err := os.Stat(path + ".3"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a third old file exists: %v", err)
	}
	if _, err := rotating.Write([]byte("late")); err == nil {
		t.Error("a write after Close gives no error")
	}
}

func TestRotatingFileAppendsToAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("12345678"), 0o644); err != nil {
		t.Fatal(err)
	}
	rotating, err := OpenRotating(path, 10, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer rotating.Close()
	if _, err := rotating.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(path + ".1")
	current, _ := os.ReadFile(path)
	if string(old) != "12345678" || string(current) != "abc" {
		t.Fatalf("old file %q and current file %q", old, current)
	}
}

func TestRotatingFileKeepsWritingWhenARotationFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	// A folder that is not empty cannot be replaced, so no old file can move.
	for _, blocked := range []string{path + ".1", path + ".2"} {
		if err := os.MkdirAll(filepath.Join(blocked, "kept"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rotating, err := OpenRotating(path, 10, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer rotating.Close()

	for _, chunk := range []string{"aaaaaa", "bbbbbb", "cccccc"} {
		if _, err := rotating.Write([]byte(chunk)); err != nil {
			t.Fatalf("the write of %s fails: %v", chunk, err)
		}
	}
	content, _ := os.ReadFile(path)
	if string(content) != "aaaaaabbbbbbcccccc" {
		t.Fatalf("the file holds %q, want every write", content)
	}
}

func TestRotatingFileOpensAgainAfterItLostItsFile(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "logs")
	path := filepath.Join(folder, "app.log")
	rotating, err := OpenRotating(path, 10, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer rotating.Close()
	if _, err := rotating.Write([]byte("aaaaaa")); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(folder); err != nil {
		t.Fatal(err)
	}
	if _, err := rotating.Write([]byte("bbbbbb")); err == nil {
		t.Fatal("a rotation into a folder that is gone gives no error")
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := rotating.Write([]byte("cccccc")); err != nil {
		t.Fatalf("the write after the folder came back fails: %v", err)
	}
	content, _ := os.ReadFile(path)
	if string(content) != "cccccc" {
		t.Fatalf("the file holds %q, want the write after the folder came back", content)
	}
}

func TestRotatingFileGivesEachNewFileItsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	rotating, err := OpenRotating(path, 10, userOnlyPerm)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []string{"aaaaaa", "bbbbbb"} {
		if _, err := rotating.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := rotating.Close(); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{path, path + ".1"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != userOnlyPerm {
			t.Errorf("%s has the mode %v, want %v", filepath.Base(name), info.Mode().Perm(), os.FileMode(userOnlyPerm))
		}
	}
}

func TestRotatingFileNarrowsTheModeOfAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	rotating, err := OpenRotating(path, 10, userOnlyPerm)
	if err != nil {
		t.Fatal(err)
	}
	if err := rotating.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != userOnlyPerm {
		t.Errorf("the file has the mode %v, want %v", info.Mode().Perm(), os.FileMode(userOnlyPerm))
	}
}
