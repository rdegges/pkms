// Package cli wires the cobra command tree.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/fetch"
)

// Exit codes shared by lint/doctor per SPEC §8:
// 0 = clean, 1 = findings at/above the fail level, 2 = execution error.
var errFindings = errors.New("findings reported")

// ExitError carries an explicit exit code through cobra.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "pkms",
		Short:         "Deterministic manager for Obsidian-compatible markdown vaults",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("vault", "", "vault name from config (optional with a single vault)")

	root.AddCommand(
		newVersionCmd(),
		newInitCmd(),
		newDoctorCmd(),
		newSnapshotCmd(),
		newUndoCmd(),
		newHistoryCmd(),
		newLintCmd(),
		newQueryCmd(),
		newProfileCmd(),
		newIngestCmd(),
	)
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	fetch.Version = version // User-Agent carries the build version
	err := newRootCmd().Execute()
	if err == nil {
		return 0
	}
	if errors.Is(err, errFindings) {
		return 1
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		if ee.Err != nil && !errors.Is(ee.Err, errFindings) {
			fmt.Fprintln(os.Stderr, "pkms:", ee.Err)
		}
		return ee.Code
	}
	fmt.Fprintln(os.Stderr, "pkms:", err)
	return 2
}
