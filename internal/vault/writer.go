package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// syncDir flushes a directory so a rename/link is durable: without it, a
// crash can lose the directory entry AFTER a caller has durably recorded
// (acked) the write (codex adversarial finding).
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// WriteAtomic replaces path with data via temp file + rename (SPEC §6).
// Used by fixes on existing notes; overwrites are intentional here.
func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".pkms-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
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
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return syncDir(dir)
}

// CreateNewNote atomically creates dir/base.md, never overwriting: on
// collision it tries "base 2.md", "base 3.md", ... (SPEC §6). Returns the
// path actually written. Atomic no-overwrite = temp file + hard link.
func CreateNewNote(dir, base string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".pkms-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	base = strings.TrimSuffix(base, ".md")
	for i := 1; i <= 1000; i++ {
		name := base + ".md"
		if i > 1 {
			name = fmt.Sprintf("%s %d.md", base, i)
		}
		target := filepath.Join(dir, name)
		err := os.Link(tmpName, target)
		if err == nil {
			// Make the directory entry durable BEFORE the caller acks
			// the record into ingest state.
			if err := syncDir(dir); err != nil {
				return "", err
			}
			return target, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("no free filename for %q in %s after 1000 tries", base, dir)
}
