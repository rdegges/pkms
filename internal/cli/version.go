package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/profile"
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
			fmt.Fprintf(cmd.OutOrStdout(), "pkms %s (%s)\nconfig schema v%d, profile schema v%d\n",
				version, commit, config.SupportedVersion, profile.SupportedSchemaVersion)
			return nil
		},
	}
}
