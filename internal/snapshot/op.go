package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/gitx"
	"github.com/rdegges/pkms/internal/paths"
)

// OpTrailer marks op commits so history can identify them.
const OpTrailer = "Pkms-Op:"

// Op wraps a mutating command in pre/post commits with a write list, so
// `pkms undo` can revert exactly the files the op touched (SPEC §9).
type Op struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // e.g. "lint-fix", "undo"
	Vault     string    `json:"vault"`
	Started   time.Time `json:"started"`
	PreCommit string    `json:"pre_commit"`
	Files     []string  `json:"files"` // vault-relative write list
	Done      bool      `json:"done"`

	g       gitx.Git
	path    string // op file path
	written map[string]bool
}

func opsDir(vaultName string) string { return paths.StateDir("ops", vaultName) }

// Begin snapshots pre-op state and opens the write list.
func Begin(v *config.Vault, kind string, now time.Time) (*Op, error) {
	g := gitx.Git{Dir: v.Path}
	if !g.IsRepo() {
		return nil, fmt.Errorf("%s is not a git repository; run `pkms init --path %s --adopt`", v.Path, v.Path)
	}
	if g.OpInProgress() {
		return nil, fmt.Errorf("merge/rebase in progress in %s; resolve it first", v.Path)
	}

	// Commit before: isolates the op's diff from prior user edits.
	if clean, err := g.IsClean(); err != nil {
		return nil, err
	} else if !clean {
		if err := g.AddAll(); err != nil {
			return nil, err
		}
		if _, err := g.Commit(fmt.Sprintf("pre(%s)", kind)); err != nil {
			return nil, err
		}
	}
	pre, err := headOrEmpty(g)
	if err != nil {
		return nil, err
	}

	op := &Op{
		ID:        fmt.Sprintf("%s-%s", kind, now.UTC().Format("20060102T150405Z")),
		Kind:      kind,
		Vault:     v.Name,
		Started:   now.UTC(),
		PreCommit: pre,
		g:         g,
		written:   map[string]bool{},
	}
	op.path = filepath.Join(opsDir(v.Name), op.ID+".json")
	if err := op.save(); err != nil {
		return nil, err
	}
	return op, nil
}

func headOrEmpty(g gitx.Git) (string, error) {
	if !g.HasCommits() {
		return "", fmt.Errorf("repository has no commits; run `pkms snapshot` first")
	}
	out, err := g.Log("%H", 1)
	if err != nil || len(out) == 0 {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	return out[0], nil
}

// Record adds a vault-relative path to the write list (before or after the
// actual write; persisted immediately so a crash can't lose it).
func (o *Op) Record(relPath string) error {
	if o.written[relPath] {
		return nil
	}
	o.written[relPath] = true
	o.Files = append(o.Files, relPath)
	sort.Strings(o.Files)
	return o.save()
}

// End commits the op's changes with the op trailer.
func (o *Op) End(summary string) error {
	clean, err := o.g.IsClean()
	if err != nil {
		return err
	}
	if !clean {
		if err := o.g.AddAll(); err != nil {
			return err
		}
		msg := fmt.Sprintf("%s: %s\n\n%s %s", o.Kind, summary, OpTrailer, o.ID)
		if _, err := o.g.Commit(msg); err != nil {
			return err
		}
	}
	o.Done = true
	return o.save()
}

func (o *Op) save() error {
	if err := os.MkdirAll(filepath.Dir(o.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(o.path, raw, 0o644)
}

// LoadOp reads an op by ID, or the most recent one for id == "last".
func LoadOp(vaultName, id string) (*Op, error) {
	dir := opsDir(vaultName)
	if id == "last" || id == "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("no operations recorded for vault %q", vaultName)
		}
		var ids []string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
			}
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("no operations recorded for vault %q", vaultName)
		}
		sort.Strings(ids)
		id = ids[len(ids)-1]
	}
	raw, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil, fmt.Errorf("unknown operation %q for vault %q", id, vaultName)
	}
	var op Op
	if err := json.Unmarshal(raw, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

// Undo reverts exactly the op's write list to its pre-op state, as a new op
// (so undoing an undo works). Concurrent user edits to other files survive.
func Undo(v *config.Vault, id string, now time.Time) (*Op, error) {
	target, err := LoadOp(v.Name, id)
	if err != nil {
		return nil, err
	}
	if !target.Done {
		return nil, fmt.Errorf("operation %s did not complete; refusing to undo", target.ID)
	}
	if len(target.Files) == 0 {
		return nil, fmt.Errorf("operation %s wrote no files; nothing to undo", target.ID)
	}

	undoOp, err := Begin(v, "undo", now)
	if err != nil {
		return nil, err
	}
	g := gitx.Git{Dir: v.Path}
	for _, f := range target.Files {
		if err := undoOp.Record(f); err != nil {
			return nil, err
		}
	}
	if err := g.RestorePaths(target.PreCommit, target.Files); err != nil {
		return nil, err
	}
	if err := undoOp.End(fmt.Sprintf("undo(%s): %d file(s)", target.ID, len(target.Files))); err != nil {
		return nil, err
	}
	return undoOp, nil
}

// HistoryEntry is one snapshot/op commit.
type HistoryEntry struct {
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
	OpID    string `json:"op_id,omitempty"`
}

// History lists the newest n pkms commits (snapshots, pre/op/undo commits).
func History(v *config.Vault, n int) ([]HistoryEntry, error) {
	g := gitx.Git{Dir: v.Path}
	if !g.IsRepo() || !g.HasCommits() {
		return nil, nil
	}
	lines, err := g.Log("%H%x09%cI%x09%s%x09%(trailers:key=Pkms-Op,valueonly,separator=)", n)
	if err != nil {
		return nil, err
	}
	var out []HistoryEntry
	for _, l := range lines {
		parts := strings.SplitN(l, "\t", 4)
		if len(parts) < 3 {
			continue
		}
		e := HistoryEntry{Commit: parts[0][:12], Date: parts[1], Subject: parts[2]}
		if len(parts) == 4 {
			e.OpID = strings.TrimSpace(parts[3])
		}
		out = append(out, e)
	}
	return out, nil
}
