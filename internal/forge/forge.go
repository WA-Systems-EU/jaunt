// Package forge abstracts a code forge that can mint short-lived,
// single-repository credentials.
package forge

import (
	"context"
	"errors"
	"time"
)

// Repo is a repository on the forge.
type Repo struct {
	Owner         string
	Name          string
	DefaultBranch string
	CloneURL      string
}

// FullName returns owner/name.
func (r Repo) FullName() string { return r.Owner + "/" + r.Name }

// Token is a short-lived credential scoped to one repository.
type Token struct {
	Value     string
	Repo      string
	ExpiresAt time.Time
}

// Guarantees states what the forge enforces on its own.
type Guarantees struct {
	AutoExpiry        bool
	WorkflowIsolation bool
}

// ErrNotInstalled means the repository is not covered by the forge app.
var ErrNotInstalled = errors.New("repository is not installed on the forge app")

// Forge mints and revokes repository-scoped tokens.
type Forge interface {
	// Repos lists the repositories the app is installed on.
	Repos(ctx context.Context) ([]Repo, error)
	// Mint returns a token for one repository (owner/name).
	Mint(ctx context.Context, repo string) (Token, error)
	// Revoke invalidates a token; an already-dead token is not an error.
	Revoke(ctx context.Context, token string) error
	Guarantees() Guarantees
}
