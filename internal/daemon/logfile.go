package daemon

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

const (
	defaultLogMaxBytes = 10 << 20
	rotatedLogFiles    = 2
)

type rotatingFile struct {
	path     string
	maxBytes int64
	perm     os.FileMode

	// Called with the lock held, so it must not write to this file.
	onRotationFailure func(error)

	mu       sync.Mutex
	file     *os.File
	size     int64
	isClosed bool
}

// perm is the mode of each new file, the ones that a rotation makes included.
func openRotatingFile(path string, maxBytes int64, perm os.FileMode) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	rotating := &rotatingFile{path: path, maxBytes: maxBytes, perm: perm}
	if err := rotating.open(); err != nil {
		return nil, err
	}
	return rotating, nil
}

func (rotating *rotatingFile) open() error {
	file, err := os.OpenFile(rotating.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, rotating.perm)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	rotating.file, rotating.size = file, info.Size()
	return nil
}

func (rotating *rotatingFile) Write(data []byte) (int, error) {
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
func (rotating *rotatingFile) rotate() error {
	_ = rotating.file.Close()
	rotating.file = nil
	shiftErr := rotating.shiftOldFiles()
	if err := rotating.open(); err != nil {
		return err
	}
	if shiftErr != nil {
		// The count starts again, so that the next attempt comes after maxBytes more, and not at each write.
		rotating.size = 0
		if rotating.onRotationFailure != nil {
			rotating.onRotationFailure(shiftErr)
		}
	}
	return nil
}

func (rotating *rotatingFile) shiftOldFiles() error {
	for index := rotatedLogFiles; index >= 1; index-- {
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

func (rotating *rotatingFile) Close() error {
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

// lossyWriter drops what its target refuses. The reader of a pipe must go on, and a later write can succeed.
type lossyWriter struct {
	target io.Writer
	// Called at the first write of each row of writes that fail.
	onLoss   func(error)
	isLosing bool
}

func (lossy *lossyWriter) Write(data []byte) (int, error) {
	_, err := lossy.target.Write(data)
	if err != nil && !lossy.isLosing {
		lossy.onLoss(err)
	}
	lossy.isLosing = err != nil
	return len(data), nil
}
