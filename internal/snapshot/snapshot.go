// Package snapshot implements the git porcelain: whole-vault snapshots,
// op-scoped write lists, undo and history (SPEC §9).
package snapshot

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/gitx"
)

// RemoteName is the git remote pkms manages for push-only snapshots.
const RemoteName = "pkms"

// Result reports one vault's snapshot outcome.
type Result struct {
	Vault     string `json:"vault"`
	Status    string `json:"status"` // committed | clean | skipped-merge | error
	Commit    string `json:"commit,omitempty"`
	FileCount int    `json:"file_count,omitempty"`
	Pushed    bool   `json:"pushed,omitempty"`
	PushError string `json:"push_error,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// Take snapshots one vault: add -A + commit, skipping clean worktrees and
// in-progress merges/rebases; optionally pushes to the per-host branch.
func Take(v *config.Vault, now time.Time) Result {
	r := Result{Vault: v.Name}
	g := gitx.Git{Dir: v.Path}

	if !g.IsRepo() {
		r.Status = "error"
		r.Detail = "not a git repository; run `pkms init --path " + v.Path + " --adopt`"
		return r
	}
	if g.OpInProgress() {
		r.Status = "skipped-merge"
		r.Detail = "merge/rebase in progress"
		return r
	}
	clean, err := g.IsClean()
	if err != nil {
		r.Status = "error"
		r.Detail = err.Error()
		return r
	}
	if clean {
		r.Status = "clean"
	} else {
		if err := g.AddAll(); err != nil {
			r.Status = "error"
			r.Detail = err.Error()
			return r
		}
		n, err := g.ChangedFileCount()
		if err != nil {
			r.Status = "error"
			r.Detail = err.Error()
			return r
		}
		msg := fmt.Sprintf("snapshot: %d file(s) @ %s", n, now.UTC().Format(time.RFC3339))
		hash, err := g.Commit(msg)
		if err != nil {
			r.Status = "error"
			r.Detail = err.Error()
			return r
		}
		r.Status = "committed"
		r.Commit = hash
		r.FileCount = n
	}

	// Push is best-effort and never blocks the snapshot (SPEC §9).
	if v.Snapshot.Remote != "" && g.HasCommits() {
		if err := g.SetRemote(RemoteName, v.Snapshot.Remote); err != nil {
			r.PushError = err.Error()
			return r
		}
		if err := g.Push(RemoteName, "snapshots/"+Hostname()); err != nil {
			r.PushError = err.Error()
		} else {
			r.Pushed = true
		}
	}
	return r
}

var hostSanitizeRe = regexp.MustCompile(`[^a-z0-9-]+`)

// Hostname returns the sanitized per-machine branch suffix.
func Hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown-host"
	}
	h = strings.ToLower(strings.TrimSuffix(h, ".local"))
	h = hostSanitizeRe.ReplaceAllString(h, "-")
	h = strings.Trim(h, "-")
	if h == "" {
		return "unknown-host"
	}
	return h
}
