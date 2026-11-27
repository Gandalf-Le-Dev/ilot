package logs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

const (
	// A log rotates at 10 MB, and the 2 previous files stay.
	DefaultMaxBytes = 10 << 20
	rotatedFiles    = 2
)

// RotatingFile appends to a log file, and moves the file aside when it reaches its size limit.
type RotatingFile struct {
	path     string
	maxBytes int64
	perm     os.FileMode

	// Called with the lock held, so it must not write to this file.
	OnRotationFailure func(error)

	mu       sync.Mutex
	file     *os.File
	size     int64
	isClosed bool
}

// OpenRotating opens a log file for appends. perm is the mode of each new file, the ones that a rotation makes included. A file that already exists loses the bits that perm does not give.
func OpenRotating(path string, maxBytes int64, perm os.FileMode) (*RotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	rotating := &RotatingFile{path: path, maxBytes: maxBytes, perm: perm}
	if err := rotating.open(); err != nil {
		return nil, err
	}
	return rotating, nil
}

func (rotating *RotatingFile) open() error {
	file, err := os.OpenFile(rotating.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, rotating.perm)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	if err := narrowMode(file, info.Mode().Perm(), rotating.perm); err != nil {
		file.Close()
		return err
	}
	rotating.file, rotating.size = file, info.Size()
	return nil
}

// narrowMode takes the bits away that an older version of ilot gave: the mode of a new file does not reach an existing one.
func narrowMode(file *os.File, current, wanted os.FileMode) error {
	if current&^wanted == 0 {
		return nil
	}
	return file.Chmod(current & wanted)
}

func (rotating *RotatingFile) Write(data []byte) (int, error) {
	rotating.mu.Lock()
	defer rotating.mu.Unlock()
	if rotating.isClosed {
		return 0, fs.ErrClosed
	}
	if rotating.file == nil {
		// An earlier write lost the file. The cause may be gone now.
		if err := rotating.open(); err != nil {
			return 0, err
		}
	}
	if rotating.size > 0 && rotating.size+int64(len(data)) > rotating.maxBytes {
		if err := rotating.rotate(); err != nil {
			return 0, err
		}
	}
	written, err := rotating.file.Write(data)
	rotating.size += int64(written)
	return written, err
}

// rotate fails only when no file is open at the end. Old files that do not move must not end the log.
func (rotating *RotatingFile) rotate() error {
	_ = rotating.file.Close()
	rotating.file = nil
	shiftErr := rotating.shiftOldFiles()
	if err := rotating.open(); err != nil {
		return err
	}
	if shiftErr != nil {
		// The count starts again, so that the next attempt comes after maxBytes more, and not at each write.
		rotating.size = 0
		if rotating.OnRotationFailure != nil {
			rotating.OnRotationFailure(shiftErr)
		}
	}
	return nil
}

func (rotating *RotatingFile) shiftOldFiles() error {
	for index := rotatedFiles; index >= 1; index-- {
		newer := rotating.path
		if index > 1 {
			newer = fmt.Sprintf("%s.%d", rotating.path, index-1)
		}
		err := os.Rename(newer, fmt.Sprintf("%s.%d", rotating.path, index))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (rotating *RotatingFile) Close() error {
	rotating.mu.Lock()
	defer rotating.mu.Unlock()
	rotating.isClosed = true
	if rotating.file == nil {
		return nil
	}
	err := rotating.file.Close()
	rotating.file = nil
	return err
}
