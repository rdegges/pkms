package vault

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Index is the parsed-vault snapshot shared by lint and query (SPEC §8).
type Index struct {
	Root string
	// Notes maps vault-relative paths to parsed notes (markdown only,
	// excluding dot-dirs and the attachments dir).
	Notes map[string]*Note
	// Files holds every file relpath outside dot-dirs, including dotfiles
	// and non-markdown files (junk/placement rules need them).
	Files map[string]bool
	// Dirs holds every directory relpath outside dot-dirs.
	Dirs map[string]bool

	// Backlinks maps a resolved target relpath to its inbound references.
	Backlinks map[string][]BackRef

	pathByKey map[string][]string // key(relpath) -> relpaths (all files)
	noteBase  map[string][]string // key(basename sans .md) -> note relpaths
	fileBase  map[string][]string // key(basename with ext) -> file relpaths
	aliasTo   map[string][]string // key(alias) -> note relpaths
}

// BackRef is one inbound link.
type BackRef struct {
	Source string
	Link   Link
}

// WalkOptions tune vault scanning.
type WalkOptions struct {
	// AttachmentsDir is a vault-relative dir whose files are indexed but
	// never parsed as notes (profile-declared, e.g. "+").
	AttachmentsDir string
}

// key normalizes a path/name for Obsidian-compatible matching:
// Unicode NFC + case folding (SPEC §5.3d).
func key(s string) string {
	return strings.ToLower(norm.NFC.String(s))
}

// BuildIndex walks root and parses every note.
func BuildIndex(root string, opts WalkOptions) (*Index, error) {
	ix := &Index{
		Root:      root,
		Notes:     map[string]*Note{},
		Files:     map[string]bool{},
		Dirs:      map[string]bool{},
		Backlinks: map[string][]BackRef{},
		pathByKey: map[string][]string{},
		noteBase:  map[string][]string{},
		fileBase:  map[string][]string{},
		aliasTo:   map[string][]string{},
	}
	root = filepath.Clean(root)

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir // .obsidian, .git, .trash, ...
			}
			ix.Dirs[rel] = true
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks are never followed (SPEC §14)
		}
		ix.Files[rel] = true
		ix.pathByKey[key(rel)] = append(ix.pathByKey[key(rel)], rel)
		base := path.Base(rel)
		ix.fileBase[key(base)] = append(ix.fileBase[key(base)], rel)

		inAttachments := opts.AttachmentsDir != "" &&
			(rel == opts.AttachmentsDir || strings.HasPrefix(rel, opts.AttachmentsDir+"/"))
		if strings.HasSuffix(rel, ".md") && !strings.HasPrefix(base, ".") && !inAttachments {
			src, err := os.ReadFile(p)
			if err != nil {
				return fmt.Errorf("read %s: %w", rel, err)
			}
			n := ParseNote(rel, src)
			ix.Notes[rel] = n
			nb := strings.TrimSuffix(base, ".md")
			ix.noteBase[key(nb)] = append(ix.noteBase[key(nb)], rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, n := range ix.Notes {
		for _, a := range n.Aliases() {
			ix.aliasTo[key(a)] = append(ix.aliasTo[key(a)], n.RelPath)
		}
	}
	for _, rel := range ix.sortedNotePaths() {
		n := ix.Notes[rel]
		for _, l := range n.Links {
			for _, target := range ix.Resolve(rel, l) {
				ix.Backlinks[target] = append(ix.Backlinks[target], BackRef{Source: rel, Link: l})
			}
		}
	}
	return ix, nil
}

func (ix *Index) sortedNotePaths() []string {
	out := make([]string, 0, len(ix.Notes))
	for p := range ix.Notes {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// NotePaths returns all note relpaths, sorted (deterministic output order).
func (ix *Index) NotePaths() []string { return ix.sortedNotePaths() }

// Resolve returns every vault path a link matches, per SPEC §5.3.
// Empty result = broken link. More than one = ambiguous.
func (ix *Index) Resolve(fromRel string, l Link) []string {
	target := strings.TrimSpace(l.Target)
	if target == "" {
		if l.Fragment != "" {
			return []string{fromRel} // [[#heading]] self-link
		}
		return nil
	}

	if l.Kind == KindMarkdown {
		// Markdown links resolve relative to the linking file first,
		// then vault-root.
		var out []string
		for _, cand := range []string{
			path.Clean(path.Join(path.Dir(fromRel), target)),
			path.Clean(strings.TrimPrefix(target, "/")),
		} {
			out = ix.lookupPath(cand)
			if len(out) > 0 {
				return out
			}
		}
		return nil
	}

	if strings.Contains(target, "/") {
		return ix.lookupPath(path.Clean(strings.TrimPrefix(target, "/")))
	}

	// Bare-name wikilink: note basenames, then (for embeds / explicit
	// extensions) any file basename, then frontmatter aliases.
	if m := ix.noteBase[key(target)]; len(m) > 0 {
		return m
	}
	if l.Embed || path.Ext(target) != "" {
		if m := ix.fileBase[key(target)]; len(m) > 0 {
			return m
		}
	}
	return ix.aliasTo[key(target)]
}

func (ix *Index) lookupPath(cand string) []string {
	if m := ix.pathByKey[key(cand)]; len(m) > 0 {
		return m
	}
	if !strings.HasSuffix(cand, ".md") {
		if m := ix.pathByKey[key(cand+".md")]; len(m) > 0 {
			return m
		}
	}
	return nil
}

// DuplicateBasenames returns note basenames shared by 2+ files, sorted.
func (ix *Index) DuplicateBasenames() map[string][]string {
	out := map[string][]string{}
	for base, paths := range ix.noteBase {
		if len(paths) > 1 {
			sorted := append([]string(nil), paths...)
			sort.Strings(sorted)
			out[base] = sorted
		}
	}
	return out
}
