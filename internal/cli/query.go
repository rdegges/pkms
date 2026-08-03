package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/query"
)

func newQueryCmd() *cobra.Command {
	var (
		typeName  string
		wheres    []string
		text      string
		backlinks string
		orphans   bool
		jsonOut   bool
	)
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Deterministic retrieval: field filters + full-text + backlinks",
		Long: `All predicates AND-combine. --where matches frontmatter fields
(equality; list fields match when they contain the value; dotted keys reach
into nested maps). Output order is deterministic (sorted paths).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			where := map[string]string{}
			for _, w := range wheres {
				k, v, ok := strings.Cut(w, "=")
				if !ok || k == "" {
					return fmt.Errorf("--where wants key=value, got %q", w)
				}
				where[k] = v
			}
			_, prof, ix, err := lintSetup(cmd)
			if err != nil {
				return err
			}
			results := query.Run(ix, prof, query.Options{
				Type: typeName, Where: where, Text: text,
				Backlinks: backlinks, Orphans: orphans,
			})
			out := cmd.OutOrStdout()
			if jsonOut {
				if results == nil {
					results = []query.Result{} // stable shape: [] not null (SPEC §10)
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{"results": results})
			}
			for _, r := range results {
				fmt.Fprintln(out, r.Path)
			}
			if len(results) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "no matches")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&typeName, "type", "", "profile note type")
	cmd.Flags().StringArrayVar(&wheres, "where", nil, "frontmatter filter key=value (repeatable, AND)")
	cmd.Flags().StringVar(&text, "text", "", "case-insensitive substring over note bodies")
	cmd.Flags().StringVar(&backlinks, "backlinks", "", "notes linking to this vault-relative path")
	cmd.Flags().BoolVar(&orphans, "orphans", false, "notes with no inbound links")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}
