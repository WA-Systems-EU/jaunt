package agentrun

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// HostRunner runs a host command (iptables) and folds its output into the error.
func HostRunner(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
