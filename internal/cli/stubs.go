package cli

// Temporary stubs: each returns exit code 2 until its module lands.
// Replaced file-by-file as build modules are implemented.

import (
	"fmt"

	"github.com/spf13/cobra"
)

func notImplemented(name string) *cobra.Command {
	return &cobra.Command{
		Use:    name,
		Short:  "(not implemented yet)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("%s is not implemented yet", name)
		},
	}
}

func newLintCmd() *cobra.Command  { return notImplemented("lint") }
func newQueryCmd() *cobra.Command { return notImplemented("query") }
