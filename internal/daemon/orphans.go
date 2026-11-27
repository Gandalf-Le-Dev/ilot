package daemon

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"syscall"
	"time"
)

const (
	defaultOrphanGrace = 5 * time.Second
	orphanKillWait     = 2 * time.Second
	orphanPollInterval = 20 * time.Millisecond
)

// processRecord is what a daemon leaves on disk for the daemon that comes after it.
type processRecord struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}

func (supervisor *Supervisor) saveRecord() {
	if supervisor.recordFile == "" {
		return
	}
	supervisor.recordMu.Lock()
	defer supervisor.recordMu.Unlock()

	supervisor.mu.Lock()
	records := map[string]processRecord{}
	for name, target := range supervisor.entries {
		if target.run != nil && target.run.state != StateExited {
			records[name] = processRecord{PID: target.run.pid, StartedAt: target.run.startedAt}
		}
	}
	supervisor.mu.Unlock()

	if err := writeRecords(supervisor.recordFile, records); err != nil {
		supervisor.logger.Error("the process record is not written, a crash of the daemon would leave processes", "file", supervisor.recordFile, "error", err)
	}
}

func writeRecords(path string, records map[string]processRecord) error {
	encoded, err := json.Marshal(records)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// killOrphans ends the process groups that a daemon left when it died without a shutdown.
func killOrphans(recordPath string, grace time.Duration, logger *slog.Logger) {
	content, err := os.ReadFile(recordPath)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	var records map[string]processRecord
	if err == nil {
		err = json.Unmarshal(content, &records)
	}
	if err != nil {
		logger.Error("the process record of the last daemon is not readable, its processes may still run", "file", recordPath, "error", err)
		return
	}

	var groups []int
	for name, record := range records {
		if !isOrphanGroup(record) {
			continue
		}
		logger.Warn("stop a process that a dead daemon left", "process", name, "pid", record.PID)
		_ = syscall.Kill(-record.PID, syscall.SIGTERM)
		groups = append(groups, record.PID)
	}
	if stubborn := waitForGroupsToEnd(groups, grace); len(stubborn) > 0 {
		for _, group := range stubborn {
			logger.Warn("kill a process that a dead daemon left", "pid", group)
			_ = syscall.Kill(-group, syscall.SIGKILL)
		}
		if immortal := waitForGroupsToEnd(stubborn, orphanKillWait); len(immortal) > 0 {
			logger.Error("processes of a dead daemon survive SIGKILL", "pids", immortal)
		}
	}
	if err := os.Remove(recordPath); err != nil {
		logger.Error("the process record of the last daemon is not removed", "file", recordPath, "error", err)
	}
}

func isOrphanGroup(record processRecord) bool {
	// kill(-1) and kill(0) reach far more than one group.
	if record.PID <= 1 || !groupExists(record.PID) {
		return false
	}
	startedAt, isLeaderAlive := processStartTime(record.PID)
	if !isLeaderAlive {
		// The system gives the ID of a live group to no new process, so this group is the recorded one.
		return true
	}
	// Another start time means that the ID now belongs to a process that the daemon never started.
	return record.StartedAt != "" && startedAt == record.StartedAt
}

func groupExists(group int) bool {
	return syscall.Kill(-group, 0) == nil
}

func waitForGroupsToEnd(groups []int, wait time.Duration) (remaining []int) {
	deadline := time.Now().Add(wait)
	for {
		remaining = remaining[:0]
		for _, group := range groups {
			if groupExists(group) {
				remaining = append(remaining, group)
			}
		}
		if len(remaining) == 0 || time.Now().After(deadline) {
			return remaining
		}
		time.Sleep(orphanPollInterval)
	}
}
