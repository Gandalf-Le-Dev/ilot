package main

import (
	"runtime/debug"
)

// Set at build time with -ldflags "-X main.stampedVersion=...". GoReleaser stamps the version of the tag.
var stampedVersion string

const shortCommitLength = 12

// The module version of a build from a checkout, which has no tag to give.
const develVersion = "(devel)"

// versionFromBuild prefers the stamp, then the module version that go install gives, for example v0.1.0, then the commit.
// Go records no git data in a linked worktree, and none for a module that go install builds from the module cache.
func versionFromBuild(stampedVersion, moduleVersion string, buildSettings []debug.BuildSetting) string {
	if stampedVersion != "" {
		return stampedVersion
	}
	var revision string
	var hasLocalChanges bool
	for _, setting := range buildSettings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			hasLocalChanges = setting.Value == "true"
		}
	}
	if moduleVersion != "" && moduleVersion != develVersion && revision == "" {
		return moduleVersion
	}
	if revision == "" {
		return "unknown"
	}
	version := "commit " + revision[:min(len(revision), shortCommitLength)]
	if hasLocalChanges {
		version += " with local changes"
	}
	return version
}

func versionOfThisBinary() string {
	buildInfo, isAvailable := debug.ReadBuildInfo()
	if !isAvailable {
		return versionFromBuild(stampedVersion, "", nil)
	}
	return versionFromBuild(stampedVersion, buildInfo.Main.Version, buildInfo.Settings)
}
