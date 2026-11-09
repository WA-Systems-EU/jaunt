// Package mesh abstracts the private network (Tailscale) the box joins.
package mesh

import (
	"context"
	"time"
)

// Mesh creates join credentials and reports device presence.
type Mesh interface {
	CreateAuthKey(ctx context.Context, tags []string, expiry time.Duration) (string, error)
	DeviceOnline(ctx context.Context, hostname string) (bool, error)
}
