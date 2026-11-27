package ilotctl

import (
	"strings"
	"testing"

	"github.com/Gandalf-Le-Dev/ilot/internal/daemon"
)

func TestADaemonAsThisIlotExpectsItGivesNoWarning(t *testing.T) {
	status := daemon.Status{Version: "v1", ProxyAddress: "127.0.0.1:4580"}

	if warnings := daemonWarnings(status, "v1", "127.0.0.1:4580"); len(warnings) != 0 {
		t.Errorf("warnings = %q, want none", warnings)
	}
}

func TestAProxyThatCannotListenGivesAWarningWithItsError(t *testing.T) {
	status := daemon.Status{Version: "v1", ProxyError: "the proxy cannot listen on 127.0.0.1:4580: address already in use"}

	warnings := daemonWarnings(status, "v1", "127.0.0.1:4580")

	if len(warnings) != 1 || !strings.Contains(warnings[0], "address already in use") || !strings.Contains(warnings[0], "proxy.listen") {
		t.Errorf("warnings = %q, want the error of the proxy and the way out", warnings)
	}
}

func TestADaemonOfAnotherVersionOrProxyAddressGivesAWarning(t *testing.T) {
	status := daemon.Status{Version: "v1", ProxyAddress: "127.0.0.1:4580"}

	warnings := daemonWarnings(status, "v2", "127.0.0.1:9999")

	if len(warnings) != 2 || !strings.Contains(warnings[0], `"v1"`) || !strings.Contains(warnings[1], `"127.0.0.1:9999"`) {
		t.Errorf("warnings = %q, want one for the version and one for the proxy address", warnings)
	}
}

func TestADaemonThatShutsDownGivesTheHintToRetry(t *testing.T) {
	err := withRetryHint(&daemon.APIError{StatusCode: 503, Message: "the daemon shuts down"})

	if err == nil || !strings.Contains(err.Error(), "run the command again") {
		t.Errorf("error = %v, want the hint to run the command again", err)
	}
}
