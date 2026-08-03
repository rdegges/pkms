package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/lock"
	"github.com/rdegges/pkms/internal/paths"
	"github.com/rdegges/pkms/internal/snapshot"
)

// withVaultLock runs fn holding the vault's exclusive lock; a held lock is
// a clean no-op exit (overlapping cron runs must not double-mutate).
func withVaultLock(v *config.Vault, out func(format string, a ...any), fn func() error) error {
	l, err := lock.Acquire(paths.StateDir("locks", v.Name+".lock"))
	if err != nil {
		var held lock.ErrHeld
		if errors.As(err, &held) {
			out("%s: %s\n", v.Name, held.Error())
			return nil
		}
		return err
	}
	defer func() { _ = l.Release() }()
	return fn()
}

func newSnapshotCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Commit the current state of every vault (or one, with --vault)",
		Long: `Runs git add -A + commit in each vault, skipping clean worktrees and
in-progress merges. With a configured remote, pushes to the per-machine
branch snapshots/<hostname> (push failures warn, never block). Designed to
run hourly from cron.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSnapshot(cmd, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

func runSnapshot(cmd *cobra.Command, jsonOut bool) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	// Explicit --vault narrows to one; default is every vault (cron entry).
	vaults := cfg.Vaults
	if name, _ := cmd.Root().PersistentFlags().GetString("vault"); name != "" {
		v, err := cfg.Vault(name)
		if err != nil {
			return err
		}
		vaults = []config.Vault{*v}
	}
	if len(vaults) == 0 {
		return fmt.Errorf("no vaults configured; run `pkms init` first")
	}

	out := cmd.OutOrStdout()
	printf := func(format string, a ...any) { fmt.Fprintf(out, format, a...) }

	var results []snapshot.Result
	for i := range vaults {
		v := &vaults[i]
		err := withVaultLock(v, printf, func() error {
			results = append(results, snapshot.Take(v, time.Now()))
			return nil
		})
		if err != nil {
			return err
		}
	}

	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return err
		}
	} else {
		for _, r := range results {
			switch r.Status {
			case "committed":
				printf("%s: committed %d file(s) (%s)\n", r.Vault, r.FileCount, r.Commit[:12])
			case "clean":
				printf("%s: nothing to snapshot\n", r.Vault)
			case "skipped-merge":
				printf("%s: skipped (%s)\n", r.Vault, r.Detail)
			case "error":
				printf("%s: ERROR %s\n", r.Vault, r.Detail)
			}
			if r.Pushed {
				printf("%s: pushed to snapshots/%s\n", r.Vault, snapshot.Hostname())
			} else if r.PushError != "" {
				printf("%s: push failed (snapshot kept): %s\n", r.Vault, r.PushError)
			}
		}
	}
	for _, r := range results {
		if r.Status == "error" {
			return fmt.Errorf("snapshot failed for vault %q: %s", r.Vault, r.Detail)
		}
	}
	return nil
}

func newUndoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "undo [op-id|last]",
		Short: "Revert exactly the files a pkms operation touched",
		Long: `Restores the operation's own write list to its pre-operation state.
Files you edited that the operation did not touch are never affected.
Undo is itself an operation, so it can be undone.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			v, err := selectedVault(cmd, cfg)
			if err != nil {
				return err
			}
			id := "last"
			if len(args) == 1 {
				id = args[0]
			}
			out := cmd.OutOrStdout()
			printf := func(format string, a ...any) { fmt.Fprintf(out, format, a...) }
			return withVaultLock(v, printf, func() error {
				op, err := snapshot.Undo(v, id, time.Now())
				if err != nil {
					return err
				}
				printf("reverted %d file(s) (undo op %s)\n", len(op.Files), op.ID)
				return nil
			})
		},
	}
	return cmd
}

func newHistoryCmd() *cobra.Command {
	var (
		n       int
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List pkms snapshots and operations",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			v, err := selectedVault(cmd, cfg)
			if err != nil {
				return err
			}
			entries, err := snapshot.History(v, n)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}
			for _, e := range entries {
				if e.OpID != "" {
					fmt.Fprintf(out, "%s  %s  %s  [%s]\n", e.Commit, e.Date, e.Subject, e.OpID)
				} else {
					fmt.Fprintf(out, "%s  %s  %s\n", e.Commit, e.Date, e.Subject)
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&n, "limit", "n", 20, "number of entries")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}
