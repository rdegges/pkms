package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/fetch"
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
		Use:   "ingest [url-or-path]",
		Short: "Ingest a URL or file, or run configured pull ingesters",
		Long: `With an argument, fetches one URL (or reads one local file) and files it
as a clip note. Without arguments, runs every enabled [[vaults.ingesters]]
entry for the vault (all vaults without --vault — like snapshot, this is a
cron entry point). Re-runs are idempotent: records already ingested are
deduplicated, never duplicated. Records that fail schema validation are
quarantined outside the vault; inspect them with pkms doctor.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if source != "" {
					return fmt.Errorf("--source runs a configured ingester; it cannot combine with a URL/path argument")
				}
				return runIngestPush(cmd, jsonOut, args[0])
			}
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

// runIngestPush ingests one URL or local file (SPEC §19 push mode).
func runIngestPush(cmd *cobra.Command, jsonOut bool, arg string) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	v, err := selectedVault(cmd, cfg)
	if err != nil {
		return err
	}
	prof, err := profile.Load(v.Profile)
	if err != nil {
		return err
	}
	noteType := prof.Ingest.Clip
	if noteType == "" {
		return fmt.Errorf(`profile %q declares no ingest note type; add to its profile.toml:

  [ingest]
  clip = "<note type for ingested clips>"`, prof.Name)
	}

	now := time.Now()
	var rec ingest.Record
	if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
		rec, err = ingest.URLRecord(cmd.Context(), fetch.New(version), arg, noteType, now)
	} else {
		rec, err = ingest.FileRecord(arg, noteType, now)
	}
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	printf := func(format string, a ...any) { fmt.Fprintf(out, format, a...) }
	runner := &ingest.Runner{Vault: v, Profile: prof, Now: time.Now}
	var res *ingest.Result
	err = withVaultLock(v, printf, func() error {
		res, err = runner.RunPush(cmd.Context(), rec)
		return err
	})
	if err != nil {
		return err
	}
	if res == nil { // vault lock held: message already printed, clean exit
		return nil
	}

	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	switch {
	case res.New == 1:
		printf("ingested → %s\n", res.Notes[0])
	case len(res.Existing) > 0:
		printf("already ingested → %s\n", res.Existing[0])
	case res.Quarantined > 0:
		printf("record failed schema validation and was quarantined; run `pkms doctor` for details\n")
		return errFindings
	default:
		printf("%s\n", res.Summary())
	}
	return nil
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
