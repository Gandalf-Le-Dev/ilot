package main

import (
	"runtime/debug"
)

// Set at build time with -ldflags "-X main.stampedVersion=...", because Go records no git data in a linked worktree.
var stampedVersion string

const shortCommitLength = 12

func versionFromBuild(stampedVersion string, buildSettings []debug.BuildSetting) string {
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
		return versionFromBuild(stampedVersion, nil)
	}
	return versionFromBuild(stampedVersion, buildInfo.Settings)
}
