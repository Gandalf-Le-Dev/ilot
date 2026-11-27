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
	// After a boot, the process IDs of the record belong to other processes.
	BootID string `json:"boot_id,omitempty"`
	// The session of the daemon, which its processes keep. It tells them from a foreign group that got the same ID.
	Session int `json:"session,omitzero"`
}

type groupMember struct {
	pid       int
	session   int
	startedAt string
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
			records[name] = processRecord{PID: target.run.pid, StartedAt: target.run.startedAt, BootID: supervisor.bootID, Session: supervisor.session}
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

	currentBootID := bootID()
	var groups []int
	for name, record := range records {
		if record.BootID != currentBootID {
			logger.Info("a record of a process is from another boot, so its process is gone", "process", name, "pid", record.PID)
			continue
		}
		isOrphan, doubt := isOrphanGroup(record)
		if doubt != "" {
			logger.Warn("a process group may be left from a dead daemon, but nothing proves it, so it stays", "process", name, "group", record.PID, "reason", doubt)
		}
		if !isOrphan {
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

// isOrphanGroup gives a doubt when the group exists, but nothing proves that the dead daemon started it.
func isOrphanGroup(record processRecord) (isOrphan bool, doubt string) {
	// kill(-1) and kill(0) reach far more than one group.
	if record.PID <= 1 || !groupExists(record.PID) {
		return false, ""
	}
	if startedAt, isLeaderAlive := processStartTime(record.PID); isLeaderAlive {
		// Another start time means that the ID now belongs to a process that the daemon never started.
		return record.StartedAt != "" && startedAt == record.StartedAt, ""
	}
	// The leader is dead, so its ID was free for a new process, which can have made this group and left it to its children.
	if record.Session <= 0 || record.StartedAt == "" {
		return false, "the record has no session or no start time"
	}
	members, err := groupMembers(record.PID)
	if err != nil {
		return false, "the members of the group are not readable: " + err.Error()
	}
	for _, member := range members {
		if member.session == record.Session && isStartNotBefore(member.startedAt, record.StartedAt) {
			return true, ""
		}
	}
	return false, "no member is in the session of the dead daemon"
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
