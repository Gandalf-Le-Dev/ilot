package daemon

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingFileKeepsTwoOldFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "app.log")
	rotating, err := openRotatingFile(path, 10)
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
	rotating, err := openRotatingFile(path, 10)
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
