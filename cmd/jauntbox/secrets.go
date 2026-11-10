package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/WA-Systems-EU/jaunt/internal/secrets"
)

// secretsCmd is `jauntbox secrets set|list`, the laptop-side secret store.
func secretsCmd(cfgPath *string) *cobra.Command {
	root := &cobra.Command{
		Use:   "secrets",
		Short: "Manage the secrets kept on this laptop (pushed to the box by unlock)",
	}
	root.AddCommand(&cobra.Command{
		Use:   "set <KEY>",
		Short: "Store an agent credential (value read from the terminal or one line of stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !secrets.EnvKeyRE.MatchString(args[0]) {
				return fmt.Errorf("invalid key %q (want %s)", args[0], secrets.EnvKeyRE)
			}
			val, err := readSecret(cmd, args[0])
			if err != nil {
				return err
			}
			if val == "" {
				return errors.New("empty value")
			}
			if err := (secrets.Store{Dir: secrets.DirFor(*cfgPath)}).SetEnv(args[0], val); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "stored %s\n", args[0])
			return nil
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List stored secret names (never values)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := secrets.Store{Dir: secrets.DirFor(*cfgPath)}
			keys, err := s.EnvKeys()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, k := range keys {
				fmt.Fprintln(out, k)
			}
			for _, f := range []struct{ file, label string }{
				{secrets.AppKeyFile, "GitHub App private key (" + secrets.AppKeyFile + ")"},
				{secrets.AppMetaFile, "GitHub App metadata (" + secrets.AppMetaFile + ")"},
			} {
				if _, err := s.Get(f.file); err == nil {
					fmt.Fprintln(out, f.label)
				}
			}
			return nil
		},
	})
	return root
}

// readSecret reads a hidden value from a terminal, else one line from stdin.
func readSecret(cmd *cobra.Command, key string) (string, error) {
	if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(cmd.ErrOrStderr(), "Value for %s: ", key)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read value: %w", err)
	}
	return strings.TrimSpace(line), nil
}
