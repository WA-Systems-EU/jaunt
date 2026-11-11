// Package provision embeds the shared provisioning scripts.
package provision

import "embed"

// The agent image build context is listed explicitly so agent_test.sh never ships in it.
//
//go:embed dev-setup.sh dev-setup-user.sh jaunt-repos.sh
//go:embed agent/Dockerfile agent/managed-settings.json agent/jaunt-*
var FS embed.FS
