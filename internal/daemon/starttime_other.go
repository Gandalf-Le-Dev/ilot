//go:build !linux

package daemon

import (
	"os/exec"
	"strconv"
	"strings"
)

// processStartTime gives a value that differs between two processes that got the same ID one after the other.
func processStartTime(pid int) (startedAt string, isAlive bool) {
	command := exec.Command("/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	// The date must have the same text in the daemon that records it and in the daemon that compares it.
	command.Env = []string{"LC_ALL=C", "TZ=UTC"}
	output, err := command.Output()
	startedAt = strings.TrimSpace(string(output))
	if err != nil || startedAt == "" {
		return "", false
	}
	return startedAt, true
}
