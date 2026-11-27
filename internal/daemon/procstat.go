package daemon

import (
	"strconv"
	"strings"
)

type procStat struct {
	group     int
	session   int
	startedAt string
}

// parseProcStat reads /proc/<pid>/stat of Linux. It has no build tag so that its test also runs on macOS.
func parseProcStat(stat string) (parsed procStat, isAlive bool) {
	// The command name is in parentheses and can hold spaces and parentheses, so the fields are counted after the last one.
	nameEnd := strings.LastIndex(stat, ")")
	if nameEnd < 0 {
		return procStat{}, false
	}
	fields := strings.Fields(stat[nameEnd+1:])
	const stateIndex, groupIndex, sessionIndex, startTimeIndex = 0, 2, 3, 19
	if len(fields) <= startTimeIndex || fields[stateIndex] == "Z" {
		return procStat{}, false
	}
	group, groupErr := strconv.Atoi(fields[groupIndex])
	session, sessionErr := strconv.Atoi(fields[sessionIndex])
	if groupErr != nil || sessionErr != nil {
		return procStat{}, false
	}
	return procStat{group: group, session: session, startedAt: fields[startTimeIndex]}, true
}

// isTickNotBefore compares two start times of /proc, which count clock ticks since the boot.
func isTickNotBefore(startedAt, reference string) bool {
	started, startedErr := strconv.ParseUint(startedAt, 10, 64)
	referenced, referenceErr := strconv.ParseUint(reference, 10, 64)
	return startedErr == nil && referenceErr == nil && started >= referenced
}
