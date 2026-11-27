package daemon

import (
	"errors"
	"fmt"
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

	mu   sync.Mutex
	file *os.File
	size int64
}

func openRotatingFile(path string, maxBytes int64) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	rotating := &rotatingFile{path: path, maxBytes: maxBytes}
	if err := rotating.open(); err != nil {
		return nil, err
	}
	return rotating, nil
}

func (rotating *rotatingFile) open() error {
	file, err := os.OpenFile(rotating.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
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
	if rotating.file == nil {
		return 0, fs.ErrClosed
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

func (rotating *rotatingFile) rotate() error {
	closeErr := rotating.file.Close()
	rotating.file = nil
	if closeErr != nil {
		return closeErr
	}
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
	return rotating.open()
}

func (rotating *rotatingFile) Close() error {
	rotating.mu.Lock()
	defer rotating.mu.Unlock()
	if rotating.file == nil {
		return nil
	}
	err := rotating.file.Close()
	rotating.file = nil
	return err
}
