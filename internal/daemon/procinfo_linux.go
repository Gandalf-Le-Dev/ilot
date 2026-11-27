package daemon

import (
	"os"
	"strconv"
	"strings"
)

// bootID changes at each boot. Process IDs and start times of /proc start again from zero then.
func bootID() string {
	content, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}

func readProcStat(pid int) (procStat, bool) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStat{}, false
	}
	return parseProcStat(string(stat))
}

// processStartTime gives a value that differs between two processes that got the same ID one after the other.
func processStartTime(pid int) (startedAt string, isAlive bool) {
	stat, isAlive := readProcStat(pid)
	return stat.startedAt, isAlive
}

func sessionOf(pid int) (session int, isKnown bool) {
	stat, isAlive := readProcStat(pid)
	return stat.session, isAlive
}

func groupMembers(group int) ([]groupMember, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var members []groupMember
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if stat, isAlive := readProcStat(pid); isAlive && stat.group == group {
			members = append(members, groupMember{pid: pid, session: stat.session, startedAt: stat.startedAt})
		}
	}
	return members, nil
}

func isStartNotBefore(startedAt, reference string) bool {
	return isTickNotBefore(startedAt, reference)
}
