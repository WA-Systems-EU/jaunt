// Package policy is the seam through which profiles and limits reach the
// client. client.Up obtains them only through a Source; the limits returned
// are frozen into BoxInfo at `up`.
package policy

import (
	"context"

	"github.com/WA-Systems-EU/jaunt/internal/config"
)

// Policy is what a Source supplies.
type Policy struct {
	Limits   config.Limits
	Profiles map[string]config.Profile
}

// Source supplies a Policy.
type Source interface {
	Load(ctx context.Context) (Policy, error)
}

// Local is a Source backed by the local config file.
type Local struct{ Cfg config.Config }

func (l Local) Load(context.Context) (Policy, error) {
	return Policy{Limits: l.Cfg.Limits, Profiles: l.Cfg.Profiles}, nil
}
