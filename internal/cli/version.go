package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Set via -ldflags at release build time.
var (
	version = "dev"
	commit  = "none"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print pkms version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "pkms %s (%s)\n", version, commit)
			return nil
		},
	}
}
