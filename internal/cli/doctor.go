package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/gitx"
	"github.com/rdegges/pkms/internal/paths"
	"github.com/rdegges/pkms/internal/profile"
)

type checkResult struct {
	Name   string `json:"name"`
	Vault  string `json:"vault,omitempty"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail,omitempty"`
}

func newDoctorCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check config, vaults, profiles and environment health",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

func runDoctor(cmd *cobra.Command, jsonOut bool) error {
	var checks []checkResult
	ok := func(name, vault, detail string) { checks = append(checks, checkResult{name, vault, "ok", detail}) }
	warn := func(name, vault, detail string) { checks = append(checks, checkResult{name, vault, "warn", detail}) }
	fail := func(name, vault, detail string) { checks = append(checks, checkResult{name, vault, "fail", detail}) }

	cfg, err := config.Load("")
	if err != nil {
		fail("config", "", err.Error())
		return doctorReport(cmd, checks, jsonOut)
	}
	ok("config", "", cfg.Path)

	if _, err := gitx.LookPath(); err != nil {
		warn("git", "", "git not found: snapshot/undo/history are disabled (lint and query still work)")
	} else if versionOK, raw := gitx.VersionOK(); !versionOK {
		warn("git", "", fmt.Sprintf("%s is older than %d.%d", raw, gitx.MinMajor, gitx.MinMinor))
	} else {
		ok("git", "", raw)
	}

	for _, dir := range []string{filepath.Dir(cfg.Path), paths.StateDir()} {
		if err := writableDir(dir); err != nil {
			fail("dir-writable", "", dir+": "+err.Error())
		} else {
			ok("dir-writable", "", dir)
		}
	}

	for i := range cfg.Vaults {
		v := &cfg.Vaults[i]
		st, err := os.Stat(v.Path)
		switch {
		case err != nil:
			fail("vault-path", v.Name, err.Error())
			continue
		case !st.IsDir():
			fail("vault-path", v.Name, v.Path+" is not a directory")
			continue
		default:
			ok("vault-path", v.Name, v.Path)
		}
		if err := writableDir(v.Path); err != nil {
			fail("vault-writable", v.Name, err.Error())
		} else {
			ok("vault-writable", v.Name, "")
		}

		if _, err := profile.Load(v.Profile); err != nil {
			fail("profile", v.Name, err.Error())
		} else {
			ok("profile", v.Name, v.Profile)
		}

		g := gitx.Git{Dir: v.Path}
		switch {
		case !g.IsRepo():
			warn("vault-git", v.Name, "not a git repository; run `pkms init --path "+v.Path+" --adopt`")
		case !g.HasCommits():
			warn("vault-git", v.Name, "repository has no commits yet; run `pkms snapshot`")
		case g.OpInProgress():
			warn("vault-git", v.Name, "merge/rebase in progress; snapshots will skip until resolved")
		default:
			ok("vault-git", v.Name, "")
		}

		if n := countFiles(paths.StateDir("failed", v.Name)); n > 0 {
			warn("quarantine", v.Name, fmt.Sprintf("%d quarantined record(s) in %s", n, paths.StateDir("failed", v.Name)))
		} else {
			ok("quarantine", v.Name, "empty")
		}

		// Per-source ingest state files parse and carry a supported
		// version (SPEC §26).
		for _, ic := range v.Sources {
			name := "ingest-state (" + ic.Source() + ")"
			switch detail, err := checkStateFile(v.Name, ic); {
			case err != nil:
				fail(name, v.Name, err.Error())
			default:
				ok(name, v.Name, detail)
			}
		}
	}

	if stale := staleLocks(paths.StateDir("locks")); len(stale) > 0 {
		warn("locks", "", fmt.Sprintf("stale lock file(s): %v", stale))
	} else {
		ok("locks", "", "none stale")
	}

	return doctorReport(cmd, checks, jsonOut)
}

func doctorReport(cmd *cobra.Command, checks []checkResult, jsonOut bool) error {
	out := cmd.OutOrStdout()
	counts := map[string]int{}
	for _, c := range checks {
		counts[c.Status]++
	}
	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{"checks": checks, "summary": counts}); err != nil {
			return err
		}
	} else {
		for _, c := range checks {
			mark := map[string]string{"ok": "✓", "warn": "!", "fail": "✗"}[c.Status]
			scope := c.Name
			if c.Vault != "" {
				scope = c.Vault + ": " + c.Name
			}
			if c.Detail != "" {
				fmt.Fprintf(out, "%s %-28s %s\n", mark, scope, c.Detail)
			} else {
				fmt.Fprintf(out, "%s %s\n", mark, scope)
			}
		}
		fmt.Fprintf(out, "\n%d ok, %d warnings, %d failures\n", counts["ok"], counts["warn"], counts["fail"])
	}
	if counts["fail"] > 0 {
		return errFindings
	}
	return nil
}

func writableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pkms-doctor-*")
	if err != nil {
		return err
	}
	_ = f.Close()
	return os.Remove(f.Name())
}

// countFiles counts files recursively (quarantine nests per source:
// failed/<vault>/<source>/<file> — SPEC §2).
func countFiles(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// checkStateFile validates a source's NDJSON ledger header without taking
// its lock (doctor must not contend with a running ingest).
func checkStateFile(vaultName string, ic config.IngesterConfig) (string, error) {
	p := paths.StateDir("state", vaultName, strings.ReplaceAll(ic.Source(), ":", "-")+".ndjson")
	f, err := os.Open(p)
	if os.IsNotExist(err) {
		return "no runs yet", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return "empty", nil
	}
	var header struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(sc.Bytes(), &header); err != nil {
		return "", fmt.Errorf("%s: header does not parse: %v", p, err)
	}
	if header.V != 1 {
		return "", fmt.Errorf("%s: state version %d is not supported by this pkms; upgrade pkms", p, header.V)
	}
	return "ok", nil
}

func staleLocks(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var stale []string
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && time.Since(info.ModTime()) > 24*time.Hour {
			stale = append(stale, e.Name())
		}
	}
	return stale
}
