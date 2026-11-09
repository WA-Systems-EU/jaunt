// Package provision embeds the shared provisioning scripts.
package provision

import "embed"

//go:embed dev-setup.sh dev-setup-user.sh jaunt-repos.sh
var FS embed.FS
