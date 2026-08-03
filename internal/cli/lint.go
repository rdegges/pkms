package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/lint"
	_ "github.com/rdegges/pkms/internal/lint/rules" // rule registrations
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/snapshot"
	"github.com/rdegges/pkms/internal/vault"
)

// maxFixIterations is a runaway backstop only: the loop exits on lack of
// progress, and the attempted-guard prevents reapplying the same fix, but
// a file with N fixable findings legitimately needs N iterations (one fix
// per file per iteration, re-parsing between).
const maxFixIterations = 1000

func newLintCmd() *cobra.Command {
	var (
		jsonOut bool
		fix     bool
		rules   string
		failOn  string
	)
	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Check the vault against its profile's rules (deterministic, no LLM)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLint(cmd, jsonOut, fix, rules, failOn)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	cmd.Flags().BoolVar(&fix, "fix", false, "apply unambiguous idempotent repairs (wrapped in snapshot commits)")
	cmd.Flags().StringVar(&rules, "rules", "", "comma-separated rule ids to run (default: all)")
	cmd.Flags().StringVar(&failOn, "fail-on", "error", "exit 1 at/above this severity: error|warning")
	return cmd
}

func lintSetup(cmd *cobra.Command) (*config.Vault, *profile.Profile, *vault.Index, error) {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	v, err := selectedVault(cmd, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	prof, err := profile.Load(v.Profile)
	if err != nil {
		return nil, nil, nil, err
	}
	ix, err := vault.BuildIndex(v.Path, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	if err != nil {
		return nil, nil, nil, err
	}
	return v, prof, ix, nil
}

func runLint(cmd *cobra.Command, jsonOut, fix bool, rulesFlag, failOn string) error {
	if failOn != string(lint.Error) && failOn != string(lint.Warning) {
		return fmt.Errorf("--fail-on must be error or warning")
	}
	var only []string
	if rulesFlag != "" {
		only = strings.Split(rulesFlag, ",")
	}

	v, prof, ix, err := lintSetup(cmd)
	if err != nil {
		return err
	}

	findings, err := lint.Run(ix, prof, v.Lint, only)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if fix {
		printf := func(format string, a ...any) { fmt.Fprintf(out, format, a...) }
		var fixed int
		var remaining []lint.Finding
		err := withVaultLock(v, printf, func() error {
			fixed, remaining, err = applyFixes(cmd, v, prof, only, findings)
			return err
		})
		if err != nil {
			return err
		}
		if remaining != nil {
			findings = remaining
		}
		if !jsonOut && fixed > 0 {
			fmt.Fprintf(out, "applied %d fix(es)\n\n", fixed)
		}
	}

	if jsonOut {
		if findings == nil {
			findings = []lint.Finding{} // stable shape: [] not null (SPEC §8)
		}
		summary := map[string]int{"error": 0, "warning": 0}
		for _, f := range findings {
			summary[string(f.Severity)]++
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		payload := map[string]any{"vault": v.Name, "findings": findings, "summary": summary}
		if err := enc.Encode(payload); err != nil {
			return err
		}
	} else {
		printFindings(out, findings)
	}

	for _, f := range findings {
		if f.Severity == lint.Error || failOn == string(lint.Warning) {
			return errFindings
		}
	}
	return nil
}

func printFindings(out interface{ Write([]byte) (int, error) }, findings []lint.Finding) {
	if len(findings) == 0 {
		fmt.Fprintln(out, "clean: no findings")
		return
	}
	lastPath := ""
	counts := map[lint.Severity]int{}
	for _, f := range findings {
		if f.Path != lastPath {
			fmt.Fprintf(out, "\n%s\n", f.Path)
			lastPath = f.Path
		}
		loc := "     "
		if f.Line > 0 {
			loc = fmt.Sprintf("%5d", f.Line)
		}
		fixMark := " "
		if f.Fixable {
			fixMark = "F"
		}
		fmt.Fprintf(out, "  %s %s%s %-40s %s\n", loc, string(f.Severity[0:1]), fixMark, f.Rule, f.Message)
		counts[f.Severity]++
	}
	fmt.Fprintf(out, "\n%d error(s), %d warning(s). F = fixable with --fix\n",
		counts[lint.Error], counts[lint.Warning])
}

// applyFixes runs the fix loop inside a snapshot op: commit before, apply
// one fix per file per iteration (re-parsing between), commit after.
func applyFixes(cmd *cobra.Command, v *config.Vault, prof *profile.Profile, only []string, findings []lint.Finding) (int, []lint.Finding, error) {
	fixable := 0
	for _, f := range findings {
		if f.Fixable {
			fixable++
		}
	}
	if fixable == 0 {
		return 0, findings, nil
	}

	var applied int
	var out []lint.Finding
	err := func() error {
		op, err := snapshot.Begin(v, "lint-fix", time.Now())
		if err != nil {
			return err
		}
		current := findings
		// A fix that "applies" without resolving its finding would reapply
		// forever; each (path, rule, message) is attempted at most once.
		attempted := map[string]bool{}
		for iter := 0; iter < maxFixIterations; iter++ {
			ix, err := vault.BuildIndex(v.Path, vault.WalkOptions{AttachmentsDir: prof.Attachments})
			if err != nil {
				return err
			}
			touched := map[string]bool{}
			for _, f := range current {
				key := fmt.Sprintf("%s\x00%s\x00%d\x00%s", f.Path, f.Rule, f.Line, f.Message)
				if !f.Fixable || touched[f.Path] || attempted[key] {
					continue
				}
				res, err := lint.Fix(ix, prof, v.Lint, f)
				if err != nil {
					return err
				}
				if res == nil {
					continue // not applicable right now; may unblock later
				}
				// Bind the guard only to actual writes: a fix that applies
				// without resolving its finding must not reapply forever.
				attempted[key] = true
				abs := filepath.Join(v.Path, filepath.FromSlash(f.Path))
				switch {
				case res.RenameTo != "":
					if err := op.Record(f.Path); err != nil {
						return err
					}
					if err := op.Record(res.RenameTo); err != nil {
						return err
					}
					dest := filepath.Join(v.Path, filepath.FromSlash(res.RenameTo))
					if _, err := os.Stat(dest); err == nil {
						continue // never overwrite on rename
					}
					if err := os.Rename(abs, dest); err != nil {
						return err
					}
				default:
					if err := op.Record(f.Path); err != nil {
						return err
					}
					if err := vault.WriteAtomic(abs, res.NewSrc); err != nil {
						return err
					}
				}
				applied++
				touched[f.Path] = true
			}
			if len(touched) == 0 {
				break
			}
			ix2, err := vault.BuildIndex(v.Path, vault.WalkOptions{AttachmentsDir: prof.Attachments})
			if err != nil {
				return err
			}
			current, err = lint.Run(ix2, prof, v.Lint, only)
			if err != nil {
				return err
			}
		}
		out = current
		return op.End(fmt.Sprintf("%d fix(es)", applied))
	}()
	if err != nil {
		return applied, findings, err
	}
	return applied, out, nil
}
