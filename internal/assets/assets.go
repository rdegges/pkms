// Package assets implements the asset storage policy (SPEC §31.2): assets
// at or under the vault's size threshold are copied into the profile's
// attachments dir and wikilinked; larger assets stay out of the vault —
// content-addressed storage for remote bytes, referenced in place for
// local files — because big binaries bloat git history and break Obsidian
// Sync's per-file caps.
package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/rdegges/pkms/internal/profile"
)

// Policy is one vault's placement decision inputs (SPEC §31.2).
type Policy struct {
	VaultRoot string
	// AttachmentsDir is the profile's vault-relative attachments dir.
	AttachmentsDir string
	// Threshold in bytes: at or under → in-vault, over → external.
	Threshold int64
	// CASDir is the content-addressed store for over-threshold remote
	// assets ($XDG_DATA_HOME/pkms/assets).
	CASDir string
}

// Source is one asset to store. SHA256 and Size come from the emitter
// (the frozen §7 Asset contract); Open streams the bytes.
type Source struct {
	Filename string
	SHA256   string // lowercase hex over the content
	Size     int64
	Open     func() (io.ReadCloser, error)
	// LocalPath, when non-empty, is the absolute path of a file the user
	// already owns on this machine; over-threshold local assets are
	// referenced in place, never copied (SPEC §31.2).
	LocalPath string
}

// Stored is the placement outcome.
type Stored struct {
	// Path is vault-relative (slash-separated) when InVault, otherwise a
	// machine-local absolute path (CAS or reference-in-place).
	Path    string
	InVault bool
	// New reports whether this call wrote the file. Cleanup after a failed
	// note write deletes only New assets — reused ones belong to an
	// earlier note (SPEC §31.5).
	New bool
}

// Store places one asset per the policy.
func (p Policy) Store(s Source) (Stored, error) {
	if s.SHA256 == "" || s.Size < 0 || s.Open == nil {
		return Stored{}, fmt.Errorf("asset %q has no content hash/size/reader (ingester bug)", s.Filename)
	}
	if s.Size <= p.Threshold {
		return p.storeInVault(s)
	}
	if s.LocalPath != "" {
		return Stored{Path: s.LocalPath, InVault: false, New: false}, nil
	}
	return p.storeCAS(s)
}

// storeInVault copies the asset into the attachments dir under its
// sanitized original name: idempotent reuse on an identical existing file,
// deterministic " 2"/" 3"… suffix on a name collision with different
// content, and link-based no-overwrite finalization (SPEC §31.2).
func (p Policy) storeInVault(s Source) (Stored, error) {
	if p.AttachmentsDir == "" {
		return Stored{}, fmt.Errorf("profile declares no attachments dir; add `attachments = \"<folder>\"` to its profile.toml")
	}
	destDir := filepath.Join(p.VaultRoot, filepath.FromSlash(p.AttachmentsDir))
	// Symlink containment before AND after MkdirAll, same reasoning as
	// writer.confined (deliberately duplicated — the packages are siblings,
	// not layers): mkdir through a symlinked ancestor would create the
	// attachment outside the vault.
	anc := destDir
	for {
		if _, err := os.Lstat(anc); err == nil {
			break
		}
		parent := filepath.Dir(anc)
		if parent == anc {
			break
		}
		anc = parent
	}
	if err := confined(p.VaultRoot, anc); err != nil {
		return Stored{}, err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return Stored{}, err
	}
	if err := confined(p.VaultRoot, destDir); err != nil {
		return Stored{}, err
	}

	name := profile.SanitizeAssetFilename(s.Filename)
	if stem := strings.TrimSuffix(name, path.Ext(name)); strings.TrimSpace(stem) == "" {
		name = shortHash(s.SHA256) + path.Ext(name)
	}
	stem, ext := name[:len(name)-len(path.Ext(name))], path.Ext(name)
	for i := 1; i <= 100; i++ {
		cand := name
		if i > 1 {
			cand = fmt.Sprintf("%s %d%s", stem, i, ext)
		}
		target := filepath.Join(destDir, cand)
		rel := path.Join(p.AttachmentsDir, cand)
		// Two passes over the SAME candidate: losing the os.Link race means
		// the name just sprang into existence — the second pass Lstats it
		// and reuses on a content match. Advancing straight to the next
		// suffix instead would break §31.2's idempotent-reuse promise under
		// concurrency (two racing stores would land duplicate blobs).
		for attempt := 0; attempt < 2; attempt++ {
			fi, err := os.Lstat(target)
			switch {
			case err == nil && fi.Mode().IsRegular():
				sum, herr := hashFile(target)
				if herr != nil {
					return Stored{}, herr
				}
				if sum == s.SHA256 {
					return Stored{Path: rel, InVault: true, New: false}, nil
				}
				attempt = 2 // same name, different content → next suffix
				continue
			case err == nil:
				attempt = 2 // a dir/symlink squats on the name → next suffix
				continue
			case !os.IsNotExist(err):
				return Stored{}, err
			}
			linked, err := writeLinked(destDir, target, s.Open)
			if err != nil {
				return Stored{}, err
			}
			if linked {
				return Stored{Path: rel, InVault: true, New: true}, nil
			}
			// Lost the race: loop to re-examine this candidate.
		}
	}
	return Stored{}, fmt.Errorf("cannot allocate attachment name for %q in %s", name, destDir)
}

// storeCAS places an over-threshold remote asset in the content-addressed
// store: the basename IS the hash, so a REGULAR file there proves
// identity. Anything else on the name (symlink, dir) is a squatter the
// ledger must never point at — error, never fall through (SPEC §31.2).
func (p Policy) storeCAS(s Source) (Stored, error) {
	if err := os.MkdirAll(p.CASDir, 0o755); err != nil {
		return Stored{}, err
	}
	ext := path.Ext(profile.SanitizeAssetFilename(s.Filename))
	target := filepath.Join(p.CASDir, s.SHA256+ext)
	fi, err := os.Lstat(target)
	switch {
	case err == nil && fi.Mode().IsRegular():
		return Stored{Path: target, InVault: false, New: false}, nil
	case err == nil:
		return Stored{}, fmt.Errorf("asset store path %s exists but is not a regular file; refusing to use it", target)
	case !os.IsNotExist(err):
		return Stored{}, err
	}
	linked, err := writeLinked(p.CASDir, target, s.Open)
	if err != nil {
		return Stored{}, err
	}
	if !linked {
		// Lost the race: name = hash, so an identical blob just landed —
		// but verify it IS a blob before pointing the ledger at it.
		fi, err := os.Lstat(target)
		if err != nil {
			return Stored{}, err
		}
		if !fi.Mode().IsRegular() {
			return Stored{}, fmt.Errorf("asset store path %s exists but is not a regular file; refusing to use it", target)
		}
	}
	return Stored{Path: target, InVault: false, New: linked}, nil
}

// writeLinked writes the stream to a temp file in dir, fsyncs it, then
// os.Link's it to target — O_EXCL semantics, never overwriting (SPEC
// §31.2). Returns false when target sprang into existence first.
func writeLinked(dir, target string, open func() (io.ReadCloser, error)) (bool, error) {
	rc, err := open()
	if err != nil {
		return false, err
	}
	defer func() { _ = rc.Close() }()
	tmp, err := os.CreateTemp(dir, ".pkms-asset-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, rc); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Link(tmp.Name(), target); err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, syncDir(dir)
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func shortHash(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func confined(root, dir string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if realDir != realRoot && !strings.HasPrefix(realDir, realRoot+string(filepath.Separator)) {
		return fmt.Errorf("attachments dir %s escapes the vault (resolves to %s)", dir, realDir)
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
