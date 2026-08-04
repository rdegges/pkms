package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/fetch"
	"github.com/rdegges/pkms/internal/ingest"
	_ "github.com/rdegges/pkms/internal/ingest/imap" // registers "imap"
	_ "github.com/rdegges/pkms/internal/ingest/rss"  // registers "rss"
	"github.com/rdegges/pkms/internal/profile"
)

const noIngestersHelp = `%w for vault %q.` + noIngestersHelpBody

const noIngestersHelpBody = `

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
quarantined outside the vault; pkms doctor reports the count and location.
Exits 1 when any record was quarantined, so cron notices.`,
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
		rec, err = ingest.URLRecord(cmd.Context(), fetch.New(), arg, noteType, now)
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
		if err := enc.Encode(vaultIngestResult{Vault: v.Name, Sources: []*ingest.Result{res}}); err != nil {
			return err
		}
	} else {
		switch {
		case res.New == 1:
			printf("ingested → %s\n", res.Notes[0])
		case len(res.Existing) > 0:
			printf("already ingested → %s\n", res.Existing[0])
		case res.Quarantined > 0:
			printf("record failed schema validation and was quarantined; run `pkms doctor` for details\n")
		default:
			printf("%s\n", res.Summary())
		}
	}
	if res.Quarantined > 0 {
		return errFindings // exit 1 regardless of output mode (SPEC §17)
	}
	return nil
}

func runIngestPull(cmd *cobra.Command, jsonOut bool, source string) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	vaults := cfg.Vaults
	explicitVault := false
	if name, _ := cmd.Root().PersistentFlags().GetString("vault"); name != "" {
		v, err := cfg.Vault(name)
		if err != nil {
			return err
		}
		vaults = []config.Vault{*v}
		explicitVault = true
	}
	if len(vaults) == 0 {
		return fmt.Errorf("no vaults configured; run `pkms init` first")
	}
	// Report "nothing configured" whenever the user is targeting a specific
	// vault/source or there's only one vault — the request is unambiguous.
	// Only the genuine multi-vault sweep silently skips a bare vault, so one
	// ingester-less vault can't fail the whole cron run.
	strictNoSources := explicitVault || source != "" || len(vaults) == 1

	out := cmd.OutOrStdout()
	printf := func(format string, a ...any) { fmt.Fprintf(out, format, a...) }

	var (
		results     = []vaultIngestResult{} // never null in --json
		quarantined int
		failed      int  // sources that errored — reported, never aborting
		ranAny      bool // did any vault have ingesters to run?
	)
	for i := range vaults {
		v := &vaults[i]
		sources, err := selectSources(v, source)
		if err != nil {
			if !strictNoSources && errors.Is(err, errNoSources) {
				continue
			}
			return err
		}
		ranAny = true
		prof, err := profile.Load(v.Profile)
		if err != nil {
			return err
		}
		runner := &ingest.Runner{Vault: v, Profile: prof, Now: time.Now}
		vres := vaultIngestResult{Vault: v.Name, Sources: []*ingest.Result{}}
		errf := func(format string, a ...any) { fmt.Fprintf(cmd.ErrOrStderr(), format, a...) }
		lockErr := withVaultLock(v, printf, func() error {
			for _, ic := range sources {
				res, err := runner.RunSource(cmd.Context(), ic)
				if err != nil {
					// An explicitly targeted single source fails loudly and
					// immediately. In the unattended sweep, one wedged
					// source must not stop the others or the rest of the
					// cron run (like snapshot push failures, §9) — report to
					// stderr and carry on; exit 2 at the end.
					if source != "" {
						return err
					}
					failed++
					errf("%s: %s: ERROR %v\n", v.Name, ic.Source(), err)
					continue
				}
				vres.Sources = append(vres.Sources, res)
				quarantined += res.Quarantined
				if !jsonOut {
					printf("%s: %s\n", v.Name, res.Summary())
				}
			}
			return nil
		})
		if lockErr != nil {
			return lockErr
		}
		results = append(results, vres)
	}

	// A whole sweep where no vault had any ingesters is a misconfiguration,
	// not a silent success — show the help and exit 2 (SPEC §19).
	if !ranAny {
		return fmt.Errorf("%w in any configured vault.%s", errNoSources, noIngestersHelpBody)
	}

	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return err
		}
	}
	if failed > 0 {
		return &ExitError{Code: 2, Err: fmt.Errorf("%d ingester(s) failed; see the errors above", failed)}
	}
	if quarantined > 0 {
		if !jsonOut {
			printf("%d record(s) failed schema validation and were quarantined outside the vault; run `pkms doctor` to see where.\n", quarantined)
		}
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
		return nil, fmt.Errorf(noIngestersHelp, errNoSources, v.Name)
	}
	return enabled, nil
}

// errNoSources marks the "vault has no enabled ingesters" case so the
// all-vaults sweep can skip it while an explicit target still errors.
var errNoSources = errors.New("no ingesters configured")

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
