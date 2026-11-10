package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/WA-Systems-EU/jaunt/internal/secrets"
	"github.com/WA-Systems-EU/jaunt/internal/vault"
)

// maxSecretsInput bounds the bundle `secrets-load` accepts on stdin.
const maxSecretsInput = 1 << 20

// boxOnlyCmds are box commands with no laptop transition alias.
func boxOnlyCmds() []*cobra.Command {
	load := &cobra.Command{
		Use:   "secrets-load",
		Short: "Load the secret bundle sent on stdin into the box's RAM (box side, root)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() != 0 {
				return errors.New("run with sudo")
			}
			return loadSecretsFrom(cmd.InOrStdin(), vault.Dir)
		},
	}
	return []*cobra.Command{load}
}

// loadSecretsFrom reads one JSON bundle (at most maxSecretsInput bytes) and
// loads it into dir. Errors never include the input.
func loadSecretsFrom(r io.Reader, dir string) error {
	data, err := io.ReadAll(io.LimitReader(r, maxSecretsInput+1))
	if err != nil {
		return fmt.Errorf("read secrets: %w", err)
	}
	if len(data) > maxSecretsInput {
		return fmt.Errorf("secret bundle too large (limit %d bytes)", maxSecretsInput)
	}
	var b secrets.Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return errors.New("secret bundle is not valid JSON")
	}
	return vault.Load(dir, b)
}
