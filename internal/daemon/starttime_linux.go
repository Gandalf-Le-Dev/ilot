package daemon

import (
	"os"
	"strconv"
)

// processStartTime gives a value that differs between two processes that got the same ID one after the other.
func processStartTime(pid int) (startedAt string, isAlive bool) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	return startTimeInProcStat(string(stat))
}
