package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"
)

type Format int

const (
	Readable Format = iota
	Raw
)

const pollInterval = 200 * time.Millisecond

func PrintMessages(ctx context.Context, logPath string, lastLines int, follow bool, format Format, output io.Writer) error {
	content, err := os.ReadFile(logPath)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: no log yet, the app never started in this stack", logPath)
	}
	if err != nil {
		return err
	}
	completeLength := bytes.LastIndexByte(content, '\n') + 1
	completeLines := bytes.Split(content[:completeLength], []byte("\n"))
	completeLines = completeLines[:len(completeLines)-1]
	if err := printLines(completeLines[max(0, len(completeLines)-lastLines):], format, output); err != nil {
		return err
	}
	if !follow {
		return nil
	}
	return followFrom(ctx, logPath, int64(completeLength), format, output)
}

func followFrom(ctx context.Context, logPath string, offset int64, format Format, output io.Writer) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pollInterval):
		}
		var err error
		if offset, err = printNewLines(logPath, offset, format, output); err != nil {
			return err
		}
	}
}

// FollowNewMessages prints the messages written to logPath from now on, until stop, which prints the last ones.
func FollowNewMessages(logPath string, output io.Writer) (stop func() error) {
	var offset int64
	if info, err := os.Stat(logPath); err == nil {
		offset = info.Size()
	}
	stopRequested := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stopRequested:
				_, err := printNewLines(logPath, offset, Readable, output)
				result <- err
				return
			case <-time.After(pollInterval):
			}
			var err error
			if offset, err = printNewLines(logPath, offset, Readable, output); err != nil {
				result <- err
				return
			}
		}
	}()
	return func() error {
		close(stopRequested)
		return <-result
	}
}

func printNewLines(logPath string, offset int64, format Format, output io.Writer) (int64, error) {
	info, err := os.Stat(logPath)
	if errors.Is(err, fs.ErrNotExist) {
		return offset, nil
	}
	if err != nil {
		return offset, err
	}
	if info.Size() < offset {
		offset = 0
	}
	if info.Size() == offset {
		return offset, nil
	}
	newContent, err := readFrom(logPath, offset)
	if err != nil {
		return offset, err
	}
	completeLength := bytes.LastIndexByte(newContent, '\n') + 1
	if completeLength == 0 {
		return offset, nil
	}
	newLines := bytes.Split(newContent[:completeLength-1], []byte("\n"))
	if err := printLines(newLines, format, output); err != nil {
		return offset, err
	}
	return offset + int64(completeLength), nil
}

func readFrom(logPath string, offset int64) ([]byte, error) {
	logFile, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()
	if _, err := logFile.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(logFile)
}

func printLines(lines [][]byte, format Format, output io.Writer) error {
	for _, line := range lines {
		message := string(line)
		if format == Readable {
			message = readableMessage(message)
		}
		if _, err := fmt.Fprintln(output, message); err != nil {
			return err
		}
	}
	return nil
}

// An app that logs JSON gets one readable line per record: the time, the level and the message come first.
func readableMessage(message string) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(message), &fields); err != nil || fields == nil {
		return message
	}
	var parts []string
	for _, leadingKey := range [][]string{{"time"}, {"level"}, {"msg", "message"}} {
		for _, key := range leadingKey {
			if value, found := fields[key]; found {
				parts = append(parts, readableValue(value))
				delete(fields, key)
			}
		}
	}
	remainingKeys := make([]string, 0, len(fields))
	for key := range fields {
		remainingKeys = append(remainingKeys, key)
	}
	slices.Sort(remainingKeys)
	for _, key := range remainingKeys {
		parts = append(parts, key+"="+readableValue(fields[key]))
	}
	return strings.Join(parts, " ")
}

func readableValue(value json.RawMessage) string {
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		return text
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return string(value)
	}
	return compact.String()
}
