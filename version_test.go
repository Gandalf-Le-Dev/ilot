package main

import (
	"runtime/debug"
	"testing"
)

func TestVersionFromBuild(t *testing.T) {
	commitSettings := []debug.BuildSetting{{Key: "vcs.revision", Value: "abcdef0123456789abcdef0123456789abcdef01"}, {Key: "vcs.modified", Value: "false"}}
	changedCheckoutSettings := []debug.BuildSetting{{Key: "vcs.revision", Value: "abcdef0123456789abcdef0123456789abcdef01"}, {Key: "vcs.modified", Value: "true"}}
	testCases := []struct {
		name           string
		stampedVersion string
		moduleVersion  string
		buildSettings  []debug.BuildSetting
		wantVersion    string
	}{
		{name: "stamped build", stampedVersion: "v0.1.0", moduleVersion: "(devel)", buildSettings: commitSettings, wantVersion: "v0.1.0"},
		{name: "build from a clean checkout", stampedVersion: "", moduleVersion: "(devel)", buildSettings: commitSettings, wantVersion: "commit abcdef012345"},
		{name: "build from a changed checkout", stampedVersion: "", moduleVersion: "(devel)", buildSettings: changedCheckoutSettings, wantVersion: "commit abcdef012345 with local changes"},
		{name: "go install of a tag", stampedVersion: "", moduleVersion: "v0.1.0", buildSettings: nil, wantVersion: "v0.1.0"},
		{name: "build without git data", stampedVersion: "", moduleVersion: "(devel)", buildSettings: nil, wantVersion: "unknown"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := versionFromBuild(testCase.stampedVersion, testCase.moduleVersion, testCase.buildSettings); got != testCase.wantVersion {
				t.Errorf("versionFromBuild = %q, want %q", got, testCase.wantVersion)
			}
		})
	}
}
