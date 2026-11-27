//go:build !linux

package daemon

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The date must have the same text in the daemon that records it and in the daemon that compares it.
var psEnvironment = []string{"LC_ALL=C", "TZ=UTC"}

// bootID changes at each boot. macOS has it, and the systems without it give an empty text at each boot.
func bootID() string {
	// Not kern.boottime: that value moves when the clock is set.
	id, err := syscall.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return ""
	}
	return id
}

// processStartTime gives a value that differs between two processes that got the same ID one after the other.
func processStartTime(pid int) (startedAt string, isAlive bool) {
	command := exec.Command("/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	command.Env = psEnvironment
	output, err := command.Output()
	startedAt = strings.Join(strings.Fields(string(output)), " ")
	if err != nil || startedAt == "" {
		return "", false
	}
	return startedAt, true
}

func sessionOf(pid int) (session int, isKnown bool) {
	session, err := syscall.Getsid(pid)
	return session, err == nil
}

func groupMembers(group int) ([]groupMember, error) {
	command := exec.Command("/bin/ps", "-axo", "pid=,pgid=,stat=,lstart=")
	command.Env = psEnvironment
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	var members []groupMember
	for line := range strings.Lines(string(output)) {
		fields := strings.Fields(line)
		const pidIndex, groupIndex, stateIndex, startIndex = 0, 1, 2, 3
		if len(fields) <= startIndex || fields[groupIndex] != strconv.Itoa(group) || strings.HasPrefix(fields[stateIndex], "Z") {
			continue
		}
		pid, err := strconv.Atoi(fields[pidIndex])
		if err != nil {
			continue
		}
		session, isKnown := sessionOf(pid)
		if !isKnown {
			continue
		}
		members = append(members, groupMember{pid: pid, session: session, startedAt: strings.Join(fields[startIndex:], " ")})
	}
	return members, nil
}

func isStartNotBefore(startedAt, reference string) bool {
	const psDate = "Mon Jan 2 15:04:05 2006"
	started, startedErr := time.Parse(psDate, startedAt)
	referenced, referenceErr := time.Parse(psDate, reference)
	return startedErr == nil && referenceErr == nil && !started.Before(referenced)
}
