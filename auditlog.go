package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Gandalf-Le-Dev/ilot/internal/logs"
)

// auditLog gets one JSON record for each action of the command, in ilot.log of the state folder. main sets it before the command runs.
var auditLog = slog.New(slog.DiscardHandler)

// The log is for its user only: its records name folders and commands.
const auditLogPerm = 0o600

// openAuditLog gives every record the command, its folder and its process. A command that changes nothing writes no record, so it makes no file.
func openAuditLog(command string) (closeLog func()) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return func() {}
	}
	currentDir, _ := os.Getwd()
	file := &lazyLogFile{path: filepath.Join(stateDirOf(homeDir), "ilot.log")}
	auditLog = slog.New(slog.NewJSONHandler(file, nil)).With("cmd", command, "args", strings.Join(os.Args[1:], " "), "cwd", currentDir, "pid", os.Getpid())
	return file.close
}

// lazyLogFile opens its file at the first write. A record that cannot be written is lost, and the command goes on.
type lazyLogFile struct {
	path string
	once sync.Once
	file *logs.RotatingFile
	err  error
}

func (lazy *lazyLogFile) Write(record []byte) (int, error) {
	lazy.once.Do(func() {
		lazy.file, lazy.err = logs.OpenRotating(lazy.path, logs.DefaultMaxBytes, auditLogPerm)
	})
	if lazy.err != nil {
		return 0, lazy.err
	}
	return lazy.file.Write(record)
}

func (lazy *lazyLogFile) close() {
	if lazy.file != nil {
		lazy.file.Close()
	}
}
