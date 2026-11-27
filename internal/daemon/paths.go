package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// A unix socket path holds 104 bytes at most on macOS and 108 on Linux.
const longestSocketPath = 100

var (
	ErrAlreadyRunning     = errors.New("another ilot daemon holds the lock")
	ErrUnsafeSocketFolder = errors.New("the socket folder is not a folder of the current user with the mode 0700")
)

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
	return filepath.Join(fallbackSocketFolder(), hex.EncodeToString(sum[:8])+".sock")
}

func fallbackSocketFolder() string {
	return filepath.Join("/tmp", "ilot-"+strconv.Itoa(os.Getuid()))
}

// checkSocketFolder guards the fallback: anyone can make a folder in /tmp before ilot does, and then own the socket in it.
func checkSocketFolder(socketPath string) error {
	if folder := filepath.Dir(socketPath); folder == fallbackSocketFolder() {
		return checkPrivateFolder(folder)
	}
	return nil
}

// A folder that does not exist is safe: the daemon makes it, and the client finds no daemon.
func checkPrivateFolder(folder string) error {
	info, err := os.Lstat(folder)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	owner, hasOwner := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !hasOwner || int(owner.Uid) != os.Getuid() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: %s", ErrUnsafeSocketFolder, folder)
	}
	return nil
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
