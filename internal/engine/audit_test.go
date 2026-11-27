package engine

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

type auditRecord struct {
	Message  string `json:"msg"`
	Process  string `json:"process"`
	Call     string `json:"call"`
	PortName string `json:"port_name"`
	Port     int    `json:"port"`
	Wanted   *bool  `json:"wanted"`
	Command  string `json:"cmd"`
	Stack    string `json:"stack"`
}

func withAuditLog(environment testEnvironment) *bytes.Buffer {
	var records bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&records, nil)).With("cmd", "start", "stack", environment.featureStack)
	environment.executor.Logger = logger
	environment.executor.Supervisor = AuditedSupervisor{ProcessSupervisor: environment.supervisor, Logger: logger}
	return &records
}

func readAuditRecords(t *testing.T, records *bytes.Buffer) []auditRecord {
	t.Helper()
	var parsed []auditRecord
	scanner := bufio.NewScanner(records)
	for scanner.Scan() {
		var record auditRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("the audit log holds a line that is not JSON: %q: %v", scanner.Text(), err)
		}
		parsed = append(parsed, record)
	}
	return parsed
}

func TestAStartWritesARecordForEachPortEachStateChangeAndEachDaemonCall(t *testing.T) {
	environment := newTestEnvironment(t)
	records := withAuditLog(environment)

	environment.start(t, environment.featureStack, StartOptions{}, "publicapi")

	parsed := readAuditRecords(t, records)
	has := func(matches func(auditRecord) bool) bool { return slices.ContainsFunc(parsed, matches) }
	for _, processName := range environment.supervisor.startedInOrder {
		if !has(func(record auditRecord) bool {
			return record.Message == "daemon call" && record.Call == "put" && record.Process == processName
		}) {
			t.Errorf("no record of the put of %s in %+v", processName, parsed)
		}
		if !has(func(record auditRecord) bool {
			return record.Message == "membership changed" && record.Process == processName && record.Wanted != nil && *record.Wanted
		}) {
			t.Errorf("no record of the membership of %s in %+v", processName, parsed)
		}
	}
	if port := environment.appPort(t, environment.featureStack, "publicapi"); !has(func(record auditRecord) bool {
		return record.Message == "port given" && record.Process == "publicapi.proj-1234.shop" && record.PortName == "http" && record.Port == port
	}) {
		t.Errorf("no record of the port %d of publicapi.proj-1234.shop in %+v", port, parsed)
	}
	if !has(func(record auditRecord) bool { return record.Message == "state saved" }) || !has(func(record auditRecord) bool { return record.Call == "routes" }) {
		t.Errorf("records = %+v, want the save of the state and the call that sends the routes", parsed)
	}
	for _, record := range parsed {
		if record.Command != "start" || record.Stack != environment.featureStack {
			t.Errorf("record %+v lacks the command or the stack", record)
		}
	}
	if strings.Contains(environment.output.String(), `"msg"`) {
		t.Errorf("output = %q, want no audit record in the progress lines", environment.output.String())
	}
}

func TestAStopWritesTheEndOfTheMembership(t *testing.T) {
	environment := newTestEnvironment(t)
	environment.start(t, environment.base, StartOptions{}, "standalone")
	records := withAuditLog(environment)

	if err := environment.executor.Stop(t.Context(), environment.base, []string{"standalone"}); err != nil {
		t.Fatal(err)
	}

	parsed := readAuditRecords(t, records)
	if !slices.ContainsFunc(parsed, func(record auditRecord) bool {
		return record.Message == "membership changed" && record.Process == "standalone.base.shop" && record.Wanted != nil && !*record.Wanted
	}) || !slices.ContainsFunc(parsed, func(record auditRecord) bool {
		return record.Call == "delete" && record.Process == "standalone.base.shop"
	}) {
		t.Errorf("records = %+v, want the end of the membership and the delete of standalone.base.shop", parsed)
	}
}
