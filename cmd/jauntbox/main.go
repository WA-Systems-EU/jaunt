// Command jauntbox brings up and tears down a remote Hetzner workspace.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/WA-Systems-EU/jaunt/internal/config"
)

func newRootCmd() *cobra.Command {
	var cfgPath string
	root := &cobra.Command{
		Use:           "jauntbox",
		Short:         "On-demand remote workspace on Hetzner",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&cfgPath, "config", config.DefaultPath(), "path to the jauntbox config file")
	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "jauntbox:", err)
		os.Exit(1)
	}
}
