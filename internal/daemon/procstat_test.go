package daemon

import "testing"

func TestParseProcStat(t *testing.T) {
	const fieldsAfterTheName = " 1 4242 4141 0 -1 4194304 120 0 0 0 1 2 0 0 20 0 1 0 987654 2625536 180 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 3 0 0 0 0 0"
	cases := []struct {
		name          string
		stat          string
		wantStartedAt string
		wantAlive     bool
	}{
		{"plain name", "4242 (sleep) S" + fieldsAfterTheName, "987654", true},
		{"name with spaces and parentheses", "4242 (my app) (v2)) R" + fieldsAfterTheName, "987654", true},
		{"zombie", "4242 (sleep) Z" + fieldsAfterTheName, "", false},
		{"cut short", "4242 (sleep) S 1 4242", "", false},
		{"empty", "", "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parsed, isAlive := parseProcStat(testCase.stat)
			if parsed.startedAt != testCase.wantStartedAt || isAlive != testCase.wantAlive {
				t.Fatalf("got %q and %v, want %q and %v", parsed.startedAt, isAlive, testCase.wantStartedAt, testCase.wantAlive)
			}
			if isAlive && (parsed.group != 4242 || parsed.session != 4141) {
				t.Fatalf("got the group %d and the session %d, want 4242 and 4141", parsed.group, parsed.session)
			}
		})
	}
}

func TestIsTickNotBefore(t *testing.T) {
	if !isTickNotBefore("1000", "999") || !isTickNotBefore("1000", "1000") {
		t.Error("a later or equal start time is refused")
	}
	// As texts, "999" comes after "1000".
	if isTickNotBefore("999", "1000") || isTickNotBefore("", "1000") || isTickNotBefore("1000", "") {
		t.Error("an earlier or unknown start time is accepted")
	}
}
