package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"dev/internal/appconfig"
	"dev/internal/bootstrap"
)

func TestAppImageReferencesListsTheContainerImageOfEveryApp(t *testing.T) {
	config := appconfig.Config{Apps: map[string]appconfig.App{
		"b-app":   {Container: appconfig.Container{Image: "reg.example/b:1"}},
		"a-app":   {Container: appconfig.Container{Image: "reg.example/a:1"}},
		"process": {Run: "make run"},
	}}

	if got, want := appImageReferences(config), []string{"reg.example/a:1", "reg.example/b:1"}; !slices.Equal(got, want) {
		t.Errorf("appImageReferences = %v, want %v", got, want)
	}
}

func TestRunCommandErrorOmitsStderrWhenAStdinWasGiven(t *testing.T) {
	const token = "tok-SECRET-0123456789"

	_, err := runCommand(context.Background(), strings.NewReader(token), "sh", "-c", "cat >&2; exit 1")

	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error contains the stdin content: %v", err)
	}
	var exitError bootstrap.ExitError
	if !errors.As(err, &exitError) || exitError.Code != 1 {
		t.Errorf("err = %v, want the exit code 1", err)
	}
}

func TestRunCommandErrorKeepsStderrWhenNoStdinWasGiven(t *testing.T) {
	_, err := runCommand(context.Background(), nil, "sh", "-c", "echo broken >&2; exit 1")

	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("err = %v, want the stderr", err)
	}
}
