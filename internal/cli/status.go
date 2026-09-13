package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/gitx"
	"github.com/rdegges/pkms/internal/ingest"
	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/paths"
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

type inboxStatus struct {
	Status          string   `json:"status"`
	Folders         []string `json:"folders"`
	Count           *int     `json:"count"`
	OldestCreatedAt *string  `json:"oldest_created_at"`
	UndatedCount    *int     `json:"undated_count"`
	Detail          string   `json:"detail,omitempty"`
}

type snapshotStatus struct {
	Status              string  `json:"status"`
	LatestCommit        *string `json:"latest_commit"`
	LatestCommitAt      *string `json:"latest_commit_at"`
	Dirty               *bool   `json:"dirty"`
	OperationInProgress *bool   `json:"operation_in_progress"`
	LastRunAt           *string `json:"last_run_at"`
	Detail              string  `json:"detail"`
}

type quarantineStatus struct {
	Status string `json:"status"`
	Files  *int   `json:"files"`
	Detail string `json:"detail,omitempty"`
}

type vaultStatus struct {
	Vault   string      `json:"vault"`
	Profile string      `json:"profile"`
	Inbox   inboxStatus `json:"inbox"`
	Ingest  struct {
		LastSuccessAt *string `json:"last_success_at"`
		Detail        string  `json:"detail"`
	} `json:"ingest"`
	Snapshot   snapshotStatus   `json:"snapshot"`
	Quarantine quarantineStatus `json:"quarantine"`
	Checks     []checkResult    `json:"checks"`
}

func newStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show capture backlog, local recovery evidence and quarantine",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			v, err := selectedVault(cmd, cfg)
			if err != nil {
				return err
			}
			r, inspectionErr := collectStatus(v)
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(r); err != nil {
					return err
				}
			} else {
				if err := writeStatus(cmd.OutOrStdout(), r); err != nil {
					return err
				}
			}
			return inspectionErr
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

func collectStatus(v *config.Vault) (vaultStatus, error) {
	r := vaultStatus{Vault: v.Name, Profile: v.Profile, Checks: []checkResult{}}
	r.Ingest.Detail = "unknown: ingest run outcomes are not recorded"
	r.Snapshot.Detail = "snapshot run time is unknown: clean successful runs leave no commit"
	check := func(name, status, detail string) {
		r.Checks = append(r.Checks, checkResult{Name: name, Status: status, Detail: detail})
	}
	for _, source := range v.Sources {
		if source.Enabled {
			if _, err := ingest.Lookup(source.Type); err != nil {
				check("ingest-config", "fail", err.Error())
			}
		}
	}
	prof, err := profile.Load(v.Profile)
	if err != nil {
		r.Inbox = inboxStatus{Status: "error", Folders: []string{}, Detail: "profile could not be loaded"}
		check("profile", "fail", err.Error())
	} else {
		if err := lint.ValidateConfig(prof, v.Lint); err != nil {
			check("lint-config", "fail", err.Error())
		} else {
			check("lint-config", "ok", "lint configuration is valid; note content was not linted")
		}
		r.Inbox = inspectInbox(v.Path, prof)
		if r.Inbox.Status == "error" {
			check("inbox", "fail", r.Inbox.Detail)
		}
	}
	s, err := (gitx.Git{Dir: v.Path}).ReadStatus()
	switch {
	case err != nil:
		r.Snapshot.Status = "error"
		check("snapshot", "fail", err.Error())
	case !s.Available:
		r.Snapshot.Status = "unavailable"
		check("snapshot", "warn", s.Detail)
	default:
		r.Snapshot.Status = "ok"
		r.Snapshot.LatestCommit, r.Snapshot.LatestCommitAt = &s.Commit, &s.CommitAt
		r.Snapshot.Dirty, r.Snapshot.OperationInProgress = s.Dirty, &s.OperationInProgress
		if s.Detail != "" {
			r.Snapshot.Detail = s.Detail + "; " + r.Snapshot.Detail
		}
		if s.OperationInProgress {
			check("snapshot", "warn", "git operation in progress; snapshots skip until it is resolved")
		}
	}
	r.Quarantine = inspectQuarantine(paths.StateDir("failed", v.Name))
	switch {
	case r.Quarantine.Status == "error":
		check("quarantine", "fail", r.Quarantine.Detail)
	case r.Quarantine.Status == "unknown":
		check("quarantine", "warn", r.Quarantine.Detail)
	case *r.Quarantine.Files > 0:
		check("quarantine", "warn", "quarantined files need review; pkms doctor reports their locations")
	}
	for _, c := range r.Checks {
		if c.Status == "fail" {
			return r, fmt.Errorf("status inspection incomplete; see failed checks")
		}
	}
	for _, c := range r.Checks {
		if c.Status == "warn" {
			return r, errFindings
		}
	}
	return r, nil
}

func inspectInbox(root string, p *profile.Profile) inboxStatus {
	r := inboxStatus{Status: "unknown", Folders: []string{}}
	folders := map[string]bool{}
	for _, name := range []string{p.Ingest.Clip, p.Ingest.Asset} {
		if name == "" {
			continue
		}
		t := p.Type(name)
		if t == nil || t.Folder == "" || strings.Contains(t.Folder, "{{") {
			r.Detail = "capture folder is missing or templated; inventory cannot be determined from a static folder"
			return r
		}
		f := path.Clean(t.Folder)
		if path.IsAbs(f) || f == ".." || strings.HasPrefix(f, "../") || strings.Contains(f, "\\") {
			r.Detail = "capture folder is not a confined vault-relative path"
			return r
		}
		folders[f] = true
	}
	if len(folders) == 0 {
		r.Detail = "profile has no capture folders"
		return r
	}
	for f := range folders {
		r.Folders = append(r.Folders, f)
	}
	sort.Strings(r.Folders)
	st, err := os.Stat(root)
	if err != nil {
		r.Status, r.Detail = "error", err.Error()
		return r
	}
	if !st.IsDir() {
		r.Status, r.Detail = "error", "vault path is not a directory"
		return r
	}
	for _, folder := range r.Folders {
		if _, err := statusDirectory(root, folder); err != nil {
			r.Status, r.Detail = "error", err.Error()
			return r
		}
	}
	ix, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: p.Attachments})
	if err != nil {
		r.Status, r.Detail = "error", err.Error()
		return r
	}
	count, undated := 0, 0
	var oldest time.Time
	haveDate := false
	for _, rel := range ix.NotePaths() {
		inbox := false
		for _, f := range r.Folders {
			if f == "." || strings.HasPrefix(rel, f+"/") {
				inbox = true
				break
			}
		}
		if !inbox {
			continue
		}
		count++
		n := ix.Notes[rel]
		var created string
		if n.FM != nil {
			created, _ = n.FM.Fields["created"].(string)
		}
		date, err := time.Parse(time.RFC3339, created)
		if err != nil {
			date, err = time.Parse("2006-01-02", created)
		}
		if err != nil {
			undated++
			continue
		}
		if !haveDate || date.Before(oldest) {
			oldest = date
			haveDate = true
		}
	}
	r.Status, r.Count, r.UndatedCount = "ok", &count, &undated
	r.Detail = "created is the source-note date, not the time it entered this inbox"
	if haveDate {
		date := oldest.UTC().Format(time.RFC3339)
		r.OldestCreatedAt = &date
	}
	return r
}

func inspectQuarantine(dir string) quarantineStatus {
	r := quarantineStatus{Status: "error"}
	count := 0
	unsupported := false
	rel, err := filepath.Rel(paths.StateDir(), dir)
	if err != nil {
		r.Detail = err.Error()
		return r
	}
	exists, err := statusDirectory(paths.StateDir(), rel)
	if err != nil {
		r.Detail = err.Error()
		return r
	}
	if !exists {
		r.Status, r.Files = "ok", &count
		return r
	}
	err = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			count++
		} else if !d.IsDir() {
			unsupported = true
		}
		return nil
	})
	if err != nil {
		r.Detail = err.Error()
		return r
	}
	if unsupported {
		r.Status, r.Detail = "unknown", "quarantine contains symlinks or special files; contents were not inspected"
		return r
	}
	r.Status, r.Files = "ok", &count
	return r
}

// Check each existing component before descending. A dangling ancestor symlink
// must not become an apparently absent (and therefore empty) directory.
func statusDirectory(root, rel string) (bool, error) {
	p := root
	parts := append([]string{"."}, strings.Split(filepath.ToSlash(rel), "/")...)
	for _, part := range parts {
		p = filepath.Join(p, part)
		st, err := os.Lstat(p)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !st.IsDir() {
			return false, fmt.Errorf("%s is not a directory; symlinks are not followed", p)
		}
	}
	return true, nil
}

func writeStatus(destination io.Writer, r vaultStatus) error {
	// Build the small report before one checked write, so every output failure
	// reaches Cobra instead of being hidden by a sequence of ignored writes.
	var out strings.Builder
	fmt.Fprintf(&out, "%s (%q)\n", r.Vault, r.Profile)
	if r.Inbox.Count == nil {
		fmt.Fprintf(&out, "Inbox: %s (%q)\n", r.Inbox.Status, r.Inbox.Detail)
	} else {
		fmt.Fprintf(&out, "Inbox: %d notes", *r.Inbox.Count)
		if r.Inbox.OldestCreatedAt != nil {
			fmt.Fprintf(&out, "; oldest source-note date %s", *r.Inbox.OldestCreatedAt)
		}
		fmt.Fprintf(&out, "; %d undated\n", *r.Inbox.UndatedCount)
	}
	fmt.Fprintf(&out, "Last successful ingest: %s\n", r.Ingest.Detail)
	if r.Snapshot.LatestCommit != nil {
		dirty := "unknown"
		if r.Snapshot.Dirty != nil {
			dirty = fmt.Sprint(*r.Snapshot.Dirty)
		}
		fmt.Fprintf(&out, "Local recovery point: %s at %s; unsnapshotted changes: %s\n", (*r.Snapshot.LatestCommit)[:12], *r.Snapshot.LatestCommitAt, dirty)
		if r.Snapshot.Dirty == nil {
			fmt.Fprintf(&out, "Recovery inspection: %q\n", r.Snapshot.Detail)
		}
	} else {
		fmt.Fprintf(&out, "Local recovery point: %s\n", r.Snapshot.Status)
	}
	fmt.Fprintf(&out, "Snapshot run time: unknown (run outcomes are not recorded)\n")
	if r.Quarantine.Files != nil {
		fmt.Fprintf(&out, "Quarantine: %d files\n", *r.Quarantine.Files)
	} else {
		fmt.Fprintf(&out, "Quarantine: %s (%q)\n", r.Quarantine.Status, r.Quarantine.Detail)
	}
	for _, c := range r.Checks {
		if c.Status != "ok" {
			fmt.Fprintf(&out, "%s: %s: %q\n", c.Status, c.Name, c.Detail)
		}
	}
	n, err := io.WriteString(destination, out.String())
	if err == nil && n != out.Len() {
		return io.ErrShortWrite
	}
	return err
}
