package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/WA-Systems-EU/jaunt/internal/client"
	"github.com/WA-Systems-EU/jaunt/internal/mesh"
	"github.com/WA-Systems-EU/jaunt/internal/provider"
	"github.com/WA-Systems-EU/jaunt/internal/provider/hetzner"
)

// termPrompter is the interactive terminal Prompter; secrets are read without echo.
type termPrompter struct{ in *bufio.Reader }

func (t termPrompter) line() (string, error) {
	s, err := t.in.ReadString('\n')
	if err != nil && s == "" {
		return "", fmt.Errorf("input ended before init finished: %w", err)
	}
	return strings.TrimSpace(s), nil
}

func (t termPrompter) Ask(q, def string) (string, error) {
	if def != "" {
		fmt.Printf("%s [%s]: ", q, def)
	} else {
		fmt.Printf("%s: ", q)
	}
	s, err := t.line()
	if err != nil || s == "" {
		return def, err
	}
	return s, nil
}

func (t termPrompter) Secret(q string) (string, error) {
	fmt.Printf("%s: ", q)
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Println()
		return string(b), err
	}
	return t.line()
}

func (t termPrompter) Confirm(q string, def bool) (bool, error) {
	hint := "[Y/n]"
	if !def {
		hint = "[y/N]"
	}
	fmt.Printf("%s %s: ", q, hint)
	s, err := t.line()
	if err != nil {
		return def, err
	}
	switch strings.ToLower(s) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

func initCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Set up jauntbox: detect your settings, check your API tokens, write the config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()
			det := client.Detect(home, os.Getenv, client.LocalZone)
			lister := func(token string) provider.TypeLister { return hetzner.New(token) }
			meshCheck := func(key string) error {
				// One devices call: success means the key is accepted.
				_, err := mesh.NewTailscale(key, "", nil).DeviceOnline(context.Background(), "jauntbox-init-check")
				return err
			}
			p := termPrompter{in: bufio.NewReader(os.Stdin)}
			_, err := client.Init(cmd.Context(), p, det, lister, meshCheck, *cfgPath, client.WithOutput(cmd.OutOrStdout()))
			return err
		},
	}
}
