package remote

import (
	"strings"
	"testing"
)

func TestBaseOptsSkipHostKeyChecks(t *testing.T) {
	got := " " + strings.Join(baseOpts, " ") + " "
	for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null", "LogLevel=ERROR"} {
		if !strings.Contains(got, want) {
			t.Errorf("baseOpts %q lacks %s", got, want)
		}
	}
}
