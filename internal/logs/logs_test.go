package logs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func logLine(text string) string {
	return text + "\n"
}

func writeLog(t *testing.T, content string) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "publicapi.log")
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return logPath
}

func TestPrintMessagesPrintsTheMessageOfTheLastLines(t *testing.T) {
	logPath := writeLog(t, logLine("first")+logLine("second")+logLine("third"))
	var output bytes.Buffer

	if err := PrintMessages(t.Context(), logPath, 2, false, Readable, &output); err != nil {
		t.Fatalf("PrintMessages: %v", err)
	}

	if output.String() != "second\nthird\n" {
		t.Errorf("output = %q", output.String())
	}
}

func TestPrintMessagesPrintsPlainAndStructuredLines(t *testing.T) {
	logPath := writeLog(t, logLine("plain text")+logLine(`{"msg":"structured"}`))
	var output bytes.Buffer

	if err := PrintMessages(t.Context(), logPath, 10, false, Readable, &output); err != nil {
		t.Fatalf("PrintMessages: %v", err)
	}

	if output.String() != "plain text\nstructured\n" {
		t.Errorf("output = %q", output.String())
	}
}

func TestPrintMessagesWaitsForALineToBeComplete(t *testing.T) {
	logPath := writeLog(t, logLine("done")+"half")
	var output bytes.Buffer

	if err := PrintMessages(t.Context(), logPath, 10, false, Readable, &output); err != nil {
		t.Fatalf("PrintMessages: %v", err)
	}

	if output.String() != "done\n" {
		t.Errorf("output = %q, want the unfinished line left out", output.String())
	}
}

func TestPrintMessagesReportsAMissingLog(t *testing.T) {
	err := PrintMessages(t.Context(), filepath.Join(t.TempDir(), "missing.log"), 10, false, Readable, &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "no log yet") {
		t.Fatalf("err = %v, want a no log yet error", err)
	}
}

type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (locked *lockedBuffer) Write(content []byte) (int, error) {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return locked.buffer.Write(content)
}

func (locked *lockedBuffer) String() string {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return locked.buffer.String()
}

func TestPrintMessagesFollowsLinesAppendedLater(t *testing.T) {
	logPath := writeLog(t, logLine("before"))
	output := &lockedBuffer{}
	ctx, cancel := context.WithCancel(t.Context())
	followDone := make(chan error, 1)
	go func() { followDone <- PrintMessages(ctx, logPath, 10, true, Readable, output) }()

	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := logFile.WriteString("aft"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * pollInterval)
	if _, err := logFile.WriteString("er\n"); err != nil {
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for output.String() != "before\nafter\n" && time.Now().Before(deadline) {
		time.Sleep(pollInterval)
	}
	cancel()
	if err := <-followDone; err != nil {
		t.Errorf("PrintMessages after cancel: %v", err)
	}
	if output.String() != "before\nafter\n" {
		t.Errorf("output = %q", output.String())
	}
}

func appendLog(t *testing.T, logPath, content string) {
	t.Helper()
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	if _, err := logFile.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func TestFollowNewMessagesPrintsOnlyTheLinesWrittenAfterItStarts(t *testing.T) {
	logPath := writeLog(t, logLine("before"))
	var output bytes.Buffer

	stop := FollowNewMessages(logPath, &output)
	appendLog(t, logPath, logLine("after"))
	if err := stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if output.String() != "after\n" {
		t.Errorf("output = %q", output.String())
	}
}

func TestFollowNewMessagesWaitsForALogThatDoesNotExistYet(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "publicapi.log")
	var output bytes.Buffer

	stop := FollowNewMessages(logPath, &output)
	appendLog(t, logPath, logLine("first"))
	if err := stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if output.String() != "first\n" {
		t.Errorf("output = %q", output.String())
	}
}

func TestReadableTurnsAnAppJSONLineIntoOneLine(t *testing.T) {
	logPath := writeLog(t, logLine(`{"level":"info","msg":"listening","time":"2026-10-28T10:00:00Z","port":8080}`))
	var output bytes.Buffer
	if err := PrintMessages(t.Context(), logPath, 10, false, Readable, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "2026-10-28T10:00:00Z info listening port=8080\n" {
		t.Errorf("output = %q", output.String())
	}
}

func TestRawPrintsTheAppLineAsIs(t *testing.T) {
	appLine := `{"level":"info","msg":"listening"}`
	logPath := writeLog(t, logLine(appLine))
	var output bytes.Buffer
	if err := PrintMessages(t.Context(), logPath, 10, false, Raw, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != `{"level":"info","msg":"listening"}`+"\n" {
		t.Errorf("output = %q", output.String())
	}
}

func TestReadableKeepsAMessageThatIsNotJSON(t *testing.T) {
	logPath := writeLog(t, logLine("plain text {not json"))
	var output bytes.Buffer
	if err := PrintMessages(t.Context(), logPath, 10, false, Readable, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "plain text {not json\n" {
		t.Errorf("output = %q", output.String())
	}
}

func TestReadableSortsTheOtherKeysAndPrintsNestedValuesAsJSON(t *testing.T) {
	logPath := writeLog(t, logLine(`{"message":"done","zeta":1,"alpha":{"b":2},"level":"warn"}`))
	var output bytes.Buffer
	if err := PrintMessages(t.Context(), logPath, 10, false, Readable, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != `warn done alpha={"b":2} zeta=1`+"\n" {
		t.Errorf("output = %q", output.String())
	}
}
