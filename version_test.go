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
		buildSettings  []debug.BuildSetting
		wantVersion    string
	}{
		{name: "image build", stampedVersion: "0.1.0-1.sha.gabcdef0", buildSettings: nil, wantVersion: "0.1.0-1.sha.gabcdef0"},
		{name: "build from a clean checkout", stampedVersion: "", buildSettings: commitSettings, wantVersion: "commit abcdef012345"},
		{name: "build from a changed checkout", stampedVersion: "", buildSettings: changedCheckoutSettings, wantVersion: "commit abcdef012345 with local changes"},
		{name: "build without git data", stampedVersion: "", buildSettings: nil, wantVersion: "unknown"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := versionFromBuild(testCase.stampedVersion, testCase.buildSettings); got != testCase.wantVersion {
				t.Errorf("versionFromBuild = %q, want %q", got, testCase.wantVersion)
			}
		})
	}
}
