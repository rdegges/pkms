package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/profile"
)

func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Inspect and eject organization profiles",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List built-in profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, name := range profile.Builtins() {
				p, err := profile.Load(name)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-10s %s\n", p.Name, p.Description)
			}
			return nil
		},
	}

	eject := &cobra.Command{
		Use:   "eject <name> <dir>",
		Short: "Copy a built-in profile to a directory for customization",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := profile.Load(args[0])
			if err != nil {
				return err
			}
			if entries, err := os.ReadDir(args[1]); err == nil && len(entries) > 0 {
				return fmt.Errorf("%s is not empty", args[1])
			}
			if err := p.Eject(args[1]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"profile %q ejected to %s\npoint a vault at it: profile = %q in config\n",
				p.Name, args[1], args[1])
			return nil
		},
	}

	cmd.AddCommand(list, eject)
	return cmd
}
