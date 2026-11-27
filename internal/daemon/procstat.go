package daemon

import "strings"

// startTimeInProcStat reads /proc/<pid>/stat of Linux. It has no build tag so that its test also runs on macOS.
func startTimeInProcStat(stat string) (startedAt string, isAlive bool) {
	// The command name is in parentheses and can hold spaces and parentheses, so the fields are counted after the last one.
	nameEnd := strings.LastIndex(stat, ")")
	if nameEnd < 0 {
		return "", false
	}
	fields := strings.Fields(stat[nameEnd+1:])
	const stateIndex, startTimeIndex = 0, 19
	if len(fields) <= startTimeIndex || fields[stateIndex] == "Z" {
		return "", false
	}
	return fields[startTimeIndex], true
}
