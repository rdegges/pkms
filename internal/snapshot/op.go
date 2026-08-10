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
	"github.com/rdegges/pkms/internal/vault"
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
		Kind:      kind,
		Vault:     v.Name,
		Started:   now.UTC(),
		PreCommit: pre,
		g:         g,
		written:   map[string]bool{},
	}
	// Second-resolution IDs can collide (two quick runs); suffix until free.
	base := fmt.Sprintf("%s-%s", kind, now.UTC().Format("20060102T150405Z"))
	for i := 1; ; i++ {
		op.ID = base
		if i > 1 {
			op.ID = fmt.Sprintf("%s-%d", base, i)
		}
		op.path = filepath.Join(opsDir(v.Name), op.ID+".json")
		if _, err := os.Stat(op.path); os.IsNotExist(err) {
			break
		}
		if i > 1000 {
			return nil, fmt.Errorf("cannot allocate op id for %s", base)
		}
	}
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

// Discard removes the journal of an op that made no changes — an empty op
// would become a useless `undo last` target.
func (o *Op) Discard() error {
	err := os.Remove(o.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
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

// save writes the journal atomically (temp + rename): a crash mid-save must
// never leave a corrupt op file — undo depends on the write list.
func (o *Op) save() error {
	dir := filepath.Dir(o.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".op-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), o.path)
}

// LoadOp reads an op by ID, or the most recent one for id == "last".
// "Last" is chronological (the journals' Started timestamps) — sorting the
// IDs lexicographically would compare the KIND prefix first, so "undo-..."
// would always beat "lint-fix-..." regardless of time (codex finding).
func LoadOp(vaultName, id string) (*Op, error) {
	dir := opsDir(vaultName)
	if id == "last" || id == "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("no operations recorded for vault %q", vaultName)
		}
		var latest *Op
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			op, err := readOp(filepath.Join(dir, e.Name()))
			if err != nil {
				continue // skip corrupt journals; named lookup still works
			}
			if latest == nil || op.Started.After(latest.Started) ||
				(op.Started.Equal(latest.Started) && op.ID > latest.ID) {
				latest = op
			}
		}
		if latest == nil {
			return nil, fmt.Errorf("no operations recorded for vault %q", vaultName)
		}
		return latest, nil
	}
	op, err := readOp(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil, fmt.Errorf("unknown operation %q for vault %q", id, vaultName)
	}
	return op, nil
}

func readOp(path string) (*Op, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var op Op
	if err := json.Unmarshal(raw, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

// Undo reverts exactly the op's write list to its pre-op state, as a new op
// (so undoing an undo works). Concurrent user edits to other files survive.
// A journaled asset path still referenced by a note OUTSIDE the op's write
// list survives the undo — idempotent reuse means attachments are shared,
// and the reference scan runs at undo time because creation-time counts
// would rot (SPEC §31.5).
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
	restore, err := withoutSharedAssets(v.Path, target.Files)
	if err != nil {
		return nil, err
	}
	if len(restore) == 0 {
		return nil, fmt.Errorf("operation %s only wrote attachments other notes still reference; nothing to undo", target.ID)
	}

	undoOp, err := Begin(v, "undo", now)
	if err != nil {
		return nil, err
	}
	g := gitx.Git{Dir: v.Path}
	for _, f := range restore {
		if err := undoOp.Record(f); err != nil {
			return nil, err
		}
	}
	if err := g.RestorePaths(target.PreCommit, restore); err != nil {
		return nil, err
	}
	if err := undoOp.End(fmt.Sprintf("undo(%s): %d file(s)", target.ID, len(restore))); err != nil {
		return nil, err
	}
	return undoOp, nil
}

// withoutSharedAssets filters an op's write list down to the paths safe to
// revert: any path a note outside the write list stamps in its `assets:`
// frontmatter ledger (SPEC §31.4) is kept on disk.
func withoutSharedAssets(vaultRoot string, files []string) ([]string, error) {
	ix, err := vault.BuildIndex(vaultRoot, vault.WalkOptions{})
	if err != nil {
		return nil, err
	}
	inOp := map[string]bool{}
	for _, f := range files {
		inOp[f] = true
	}
	referenced := map[string]bool{}
	for rel, n := range ix.Notes {
		if inOp[rel] || n.FM == nil || n.FM.Fields == nil {
			continue
		}
		list, ok := n.FM.Fields["assets"].([]any)
		if !ok {
			continue
		}
		for _, item := range list {
			if p, ok := item.(string); ok {
				referenced[p] = true
			}
		}
	}
	var restore []string
	for _, f := range files {
		if !referenced[f] {
			restore = append(restore, f)
		}
	}
	return restore, nil
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
