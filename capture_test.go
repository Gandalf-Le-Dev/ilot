package main

import (
	"strings"
	"testing"
)

func TestRunCapturingOutputHoldsBothStreams(t *testing.T) {
	script := `i=0; while [ $i -lt 200 ]; do echo "out$i"; echo "err$i" >&2; i=$((i+1)); done`

	output, err := runCapturingOutput(t.Context(), "sh", "-c", script)

	if err != nil {
		t.Fatalf("runCapturingOutput: %v", err)
	}
	if got := strings.Count(output, "out"); got != 200 {
		t.Errorf("captured %d stdout lines, want 200", got)
	}
	if got := strings.Count(output, "err"); got != 200 {
		t.Errorf("captured %d stderr lines, want 200", got)
	}
}
