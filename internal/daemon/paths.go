package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// A unix socket path holds 104 bytes at most on macOS and 108 on Linux.
const longestSocketPath = 100

var ErrAlreadyRunning = errors.New("another ilot daemon holds the lock")

type Paths struct {
	StateDir string
}

func (paths Paths) LockFile() string {
	return filepath.Join(paths.StateDir, "daemon.lock")
}

func (paths Paths) LogFile() string {
	return filepath.Join(paths.StateDir, "daemon.log")
}

func (paths Paths) RecordFile() string {
	return filepath.Join(paths.StateDir, "processes.json")
}

func (paths Paths) Socket() string {
	inStateDir := filepath.Join(paths.StateDir, "daemon.sock")
	if len(inStateDir) <= longestSocketPath {
		return inStateDir
	}
	sum := sha256.Sum256([]byte(paths.StateDir))
	return filepath.Join("/tmp", "ilot-"+strconv.Itoa(os.Getuid()), hex.EncodeToString(sum[:8])+".sock")
}

// acquireLock gives the proof of life of the daemon: the kernel drops the lock when the process dies, however it dies.
func acquireLock(path string) (*os.File, error) {
	lockFile, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lockFile.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	// The process ID is for a person who reads the file. The lock is the proof.
	if err := lockFile.Truncate(0); err == nil {
		_, _ = lockFile.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return lockFile, nil
}
