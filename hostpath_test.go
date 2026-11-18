package main

import "testing"

func TestHostPathTurnsDevboxPathsIntoHostPaths(t *testing.T) {
	toHostPath := hostPathFor("/home/user", "jdoe")
	testCases := map[string]string{
		"/home/user/schemas":                   "/home/jdoe/devbox/schemas",
		"/home/user":                           "/home/jdoe/devbox",
		"/home/jdoe/devhost-legacy/conf":       "/home/jdoe/devhost-legacy/conf",
		"/etc/acme.conf.d/devhost.conf":        "/etc/acme.conf.d/devhost.conf",
		"/home/username-that-starts-like-user": "/home/username-that-starts-like-user",
	}
	for devboxPath, wantHostPath := range testCases {
		if got := toHostPath(devboxPath); got != wantHostPath {
			t.Errorf("hostPath(%q) = %q, want %q", devboxPath, got, wantHostPath)
		}
	}
}
