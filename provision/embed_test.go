package provision

import (
	"strings"
	"testing"
)

func TestEmbedsScripts(t *testing.T) {
	for _, name := range []string{"dev-setup.sh", "dev-setup-user.sh", "jaunt-repos.sh"} {
		b, err := FS.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(b) == 0 || !strings.HasPrefix(string(b), "#!/usr/bin/env bash") {
			t.Errorf("%s: empty or missing shebang", name)
		}
	}
}
