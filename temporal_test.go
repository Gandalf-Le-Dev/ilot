package main

import (
	"os"
	"path/filepath"
	"testing"
)

func fakeTemporalCLI(t *testing.T, script string) {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "temporal"), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDeletingANamespaceThatDoesNotExistIsNotAnError(t *testing.T) {
	fakeTemporalCLI(t, `echo 'time=2026-10-21T14:52:42.249 level=ERROR msg="unable to delete namespace proj-0001: Namespace proj-0001 is not found."' >&2; exit 1`)

	if err := deleteTemporalNamespace("localhost:7233")(t.Context(), "proj-0001"); err != nil {
		t.Fatalf("delete = %v, want no error for a namespace that was never created", err)
	}
}

func TestDeletingANamespaceReportsOtherFailures(t *testing.T) {
	fakeTemporalCLI(t, `echo 'failed reaching server: connection refused' >&2; exit 1`)

	if err := deleteTemporalNamespace("localhost:7233")(t.Context(), "proj-0001"); err == nil {
		t.Fatal("delete = nil, want the connection error")
	}
}
