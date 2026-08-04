package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/ingest"
	"github.com/rdegges/pkms/internal/profile"
)

const noIngestersHelp = `no ingesters configured for vault %q.

Add one to your config:

  [[vaults.ingesters]]
  type = "rss"
  name = "example"
  url  = "https://example.com/feed.xml"

or ingest a single page directly:  pkms ingest https://example.com/article`

func newIngestCmd() *cobra.Command {
	var (
		jsonOut bool
		source  string
	)
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Run configured pull ingesters (RSS, IMAP)",
		Long: `Runs every enabled [[vaults.ingesters]] entry for the vault (all vaults
without --vault — like snapshot, this is a cron entry point). Re-runs are
idempotent: records already ingested are deduplicated, never duplicated.
Records that fail schema validation are quarantined outside the vault;
inspect them with pkms doctor.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIngestPull(cmd, jsonOut, source)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	cmd.Flags().StringVar(&source, "source", "", "run a single ingester by name")
	return cmd
}

type vaultIngestResult struct {
	Vault   string           `json:"vault"`
	Sources []*ingest.Result `json:"sources"`
}

func runIngestPull(cmd *cobra.Command, jsonOut bool, source string) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}
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

	var (
		results     []vaultIngestResult
		quarantined int
	)
	for i := range vaults {
		v := &vaults[i]
		sources, err := selectSources(v, source)
		if err != nil {
			return err
		}
		prof, err := profile.Load(v.Profile)
		if err != nil {
			return err
		}
		runner := &ingest.Runner{Vault: v, Profile: prof, Now: time.Now}
		vres := vaultIngestResult{Vault: v.Name, Sources: []*ingest.Result{}}
		err = withVaultLock(v, printf, func() error {
			for _, ic := range sources {
				res, err := runner.RunSource(context.Background(), ic)
				if err != nil {
					return err
				}
				vres.Sources = append(vres.Sources, res)
				quarantined += res.Quarantined
				if !jsonOut {
					printf("%s: %s\n", v.Name, res.Summary())
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		results = append(results, vres)
	}

	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return err
		}
	}
	if quarantined > 0 {
		return errFindings // exit 1: cron must notice quarantines
	}
	return nil
}

// selectSources resolves --source against a vault's enabled ingesters.
func selectSources(v *config.Vault, source string) ([]config.IngesterConfig, error) {
	if source != "" {
		for _, ic := range v.Sources {
			if ic.Name == source {
				return []config.IngesterConfig{ic}, nil
			}
		}
		return nil, fmt.Errorf("vault %q has no ingester named %q (configured: %s)",
			v.Name, source, sourceNames(v.Sources))
	}
	var enabled []config.IngesterConfig
	for _, ic := range v.Sources {
		if ic.Enabled {
			enabled = append(enabled, ic)
		}
	}
	if len(enabled) == 0 {
		return nil, fmt.Errorf(noIngestersHelp, v.Name)
	}
	return enabled, nil
}

func sourceNames(ics []config.IngesterConfig) string {
	if len(ics) == 0 {
		return "none"
	}
	s := ""
	for i, ic := range ics {
		if i > 0 {
			s += ", "
		}
		s += ic.Name
	}
	return s
}
