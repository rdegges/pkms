package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/gitx"
	"github.com/rdegges/pkms/internal/paths"
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

type checkResult struct {
	Name   string `json:"name"`
	Vault  string `json:"vault,omitempty"`
	Status string `json:"status"` // ok | info | warn | fail
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
	info := func(name, vault, detail string) { checks = append(checks, checkResult{name, vault, "info", detail}) }
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

		prof, profErr := profile.Load(v.Profile)
		if profErr != nil {
			fail("profile", v.Name, profErr.Error())
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

		// asset-refs (SPEC §31.9): every vault-relative path stamped in an
		// `assets:` frontmatter list must exist in the vault right now. The
		// check reads ONLY `assets:` fields, so a note that never carried
		// assets can't make it green-by-omission. External (CAS/reference)
		// paths are machine-local and expected absent on other devices, so
		// they are reported informationally and never color pass/fail.
		if profErr == nil {
			assetRefsCheck(v.Name, v.Path, prof, ok, warn, info)
		}

		// Quarantine counts, per source (SPEC §26).
		totalQ := 0
		for _, ic := range v.Sources {
			qdir := paths.StateDir("failed", v.Name, strings.ReplaceAll(ic.Source(), ":", "-"))
			if n := countFiles(qdir); n > 0 {
				warn("quarantine ("+ic.Source()+")", v.Name, fmt.Sprintf("%d quarantined record(s) in %s", n, qdir))
				totalQ += n
			}
		}
		// The adhoc (push) source and any legacy files aren't tied to a
		// configured source — count the rest of the vault's failed dir too.
		if all := countFiles(paths.StateDir("failed", v.Name)); all > totalQ {
			warn("quarantine", v.Name, fmt.Sprintf("%d quarantined record(s) in %s", all-totalQ, paths.StateDir("failed", v.Name)))
		} else if all == 0 {
			ok("quarantine", v.Name, "empty")
		}

		// Per-source ingest state files parse and carry a supported
		// version + a known cursor schema (SPEC §26).
		for _, ic := range v.Sources {
			name := "ingest-state (" + ic.Source() + ")"
			switch detail, err := checkStateFile(v.Name, ic); {
			case err != nil:
				fail(name, v.Name, err.Error())
			default:
				ok(name, v.Name, detail)
			}
		}

		// Keyring reachability — ONLY when an ingester needs a secret, and
		// only as a warning (headless machines must never fail doctor).
		if needsKeyring(v) {
			if err := probeKeyring(); err != nil {
				warn("keyring", v.Name, "not reachable ("+err.Error()+"); ingesters will fall back to $PKMS_* env vars / password_cmd")
			} else {
				ok("keyring", v.Name, "reachable")
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
			mark := map[string]string{"ok": "✓", "info": "i", "warn": "!", "fail": "✗"}[c.Status]
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
		fmt.Fprintf(out, "\n%d ok, %d info, %d warnings, %d failures\n", counts["ok"], counts["info"], counts["warn"], counts["fail"])
	}
	if counts["fail"] > 0 {
		return errFindings
	}
	return nil
}

// assetRefsCheck implements the §31.9 doctor check. The green sentence:
// every vault-relative asset path stamped in an `assets:` frontmatter list
// exists in the vault right now.
func assetRefsCheck(vaultName, vaultPath string, prof *profile.Profile, ok, warn, info func(name, vault, detail string)) {
	ix, err := vault.BuildIndex(vaultPath, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	if err != nil {
		warn("asset-refs", vaultName, "could not scan the vault: "+err.Error())
		return
	}
	var inVaultMissing, externalMissing int
	var firstInVault string
	// checkPath stats one ledger entry, splitting in-vault vs external.
	checkPath := func(rel, p string) {
		if p == "" {
			return
		}
		if filepath.IsAbs(p) {
			if _, err := os.Stat(p); err != nil {
				externalMissing++
			}
			return
		}
		if _, err := os.Stat(filepath.Join(vaultPath, filepath.FromSlash(p))); err != nil {
			inVaultMissing++
			if firstInVault == "" {
				firstInVault = rel + " → " + p
			}
		}
	}
	for rel, n := range ix.Notes {
		if n.FM == nil || n.FM.Fields == nil {
			continue
		}
		// The pipeline always writes a list, but a hand-edited scalar
		// `assets: path` is a legitimate one-entry ledger — check it too,
		// so a dangling scalar can't report green (gates fail closed).
		switch v := n.FM.Fields["assets"].(type) {
		case []any:
			for _, item := range v {
				if p, isStr := item.(string); isStr {
					checkPath(rel, p)
				}
			}
		case string:
			checkPath(rel, v)
		}
	}
	// A dangling in-vault path is a warning ("moved or deleted", never
	// "lost" — the vault's git history has it).
	if inVaultMissing > 0 {
		warn("asset-refs", vaultName, fmt.Sprintf("%d in-vault attachment(s) moved or deleted (e.g. %s)", inVaultMissing, firstInVault))
	} else {
		ok("asset-refs", vaultName, "every in-vault attachment exists")
	}
	// External paths are machine-local (§31.2): absent on another synced
	// device is EXPECTED, so it is info, never a warning.
	if externalMissing > 0 {
		info("asset-refs", vaultName, fmt.Sprintf("%d external asset path(s) not present on this machine (expected on other synced devices)", externalMissing))
	}
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
		V            int    `json:"v"`
		CursorSchema string `json:"cursor_schema"`
	}
	if err := json.Unmarshal(sc.Bytes(), &header); err != nil {
		return "", fmt.Errorf("%s: header does not parse: %v", p, err)
	}
	if header.V != 1 {
		return "", fmt.Errorf("%s: state version %d is not supported by this pkms; upgrade pkms", p, header.V)
	}
	if s := header.CursorSchema; s != "" && !knownCursorSchemas[s] {
		return "", fmt.Errorf("%s: cursor schema %q is not known to this pkms; upgrade pkms", p, s)
	}
	return "ok", nil
}

// knownCursorSchemas are the cursor formats this binary can read (SPEC §26).
var knownCursorSchemas = map[string]bool{"imap/1": true, "rss/1": true}

// needsKeyring reports whether any of a vault's ingesters resolve a secret.
func needsKeyring(v *config.Vault) bool {
	for _, ic := range v.Sources {
		if ic.Type == "imap" {
			return true
		}
	}
	return false
}

// probeKeyring distinguishes an unreachable backend (headless: no D-Bus /
// Secret Service) from a normal "not found" — go-keyring returns ErrNotFound
// when the backend works but the key is absent.
func probeKeyring() error {
	_, err := keyring.Get("pkms", "pkms-doctor-probe")
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
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
