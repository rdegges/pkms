// Package profile loads organization profiles: data-only directories of
// folder templates, note-type JSON Schemas and lint configuration (SPEC §4).
package profile

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"text/template"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/rdegges/pkms/profiles"
)

// SupportedSchemaVersion is the profile format version this binary reads.
const SupportedSchemaVersion = 1

type Manifest struct {
	SchemaVersion int    `toml:"schema_version"`
	Name          string `toml:"name"`
	Description   string `toml:"description"`
	// Attachments is a vault-relative dir indexed but never note-parsed.
	Attachments string   `toml:"attachments"`
	Scaffold    []string `toml:"scaffold"`
	RootFiles   []string `toml:"root_files"`
	Types       []Type   `toml:"types"`
	Indexes     []Index  `toml:"indexes"`
	// Ingest names the note types the ingest pipeline targets (SPEC §26).
	Ingest IngestTypes `toml:"ingest"`
	// Lint holds per-rule config, interpreted by each rule's factory.
	Lint map[string]map[string]any `toml:"lint"`
}

// IngestTypes maps ingest record kinds to profile note types.
type IngestTypes struct {
	// Clip is the note type for ingested clips (URLs, feeds, email).
	Clip string `toml:"clip"`
	// Asset is the note type for ingested binaries (SPEC §31.4).
	Asset string `toml:"asset"`
}

// Type declares one note type. Order matters: classification returns the
// first type whose Scope matches (and whose RequireAnyKey trigger, if any,
// fires), so content-triggered types precede their generic fallbacks.
type Type struct {
	Name string `toml:"name"`
	// Scope is a list of doublestar globs over vault-relative paths.
	Scope []string `toml:"scope"`
	// RequireAnyKey gates the type on frontmatter key presence
	// (e.g. clip-summary = source_url OR date_clipped).
	RequireAnyKey []string `toml:"require_any_key"`
	Schema        string   `toml:"schema"`
	Template      string   `toml:"template"`
	Folder        string   `toml:"folder"`
	Filename      string   `toml:"filename"`
}

// Index declares an index-completeness contract (policy: must-link-all).
type Index struct {
	File   string `toml:"file"`
	Lists  string `toml:"lists"`
	Policy string `toml:"policy"`
}

type Profile struct {
	Manifest
	// Path is the on-disk profile dir; empty for built-ins.
	Path    string
	fsys    fs.FS
	schemas map[string]*jsonschema.Schema
}

// Builtins lists embedded profile names.
func Builtins() []string { return []string{"para", "rdegges"} }

// Load resolves nameOrPath: a built-in profile name, or a directory path.
func Load(nameOrPath string) (*Profile, error) {
	for _, b := range Builtins() {
		if nameOrPath == b {
			sub, err := fs.Sub(profiles.FS, b)
			if err != nil {
				return nil, err
			}
			return load(sub, "")
		}
	}
	st, err := os.Stat(nameOrPath)
	if err != nil {
		return nil, fmt.Errorf("profile %q: not a built-in (%s) and not a directory: %w",
			nameOrPath, strings.Join(Builtins(), ", "), err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("profile %q must be a directory", nameOrPath)
	}
	return load(os.DirFS(nameOrPath), nameOrPath)
}

func load(fsys fs.FS, diskPath string) (*Profile, error) {
	raw, err := fs.ReadFile(fsys, "profile.toml")
	if err != nil {
		return nil, fmt.Errorf("profile.toml: %w", err)
	}
	p := &Profile{Path: diskPath, fsys: fsys, schemas: map[string]*jsonschema.Schema{}}
	dec := toml.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p.Manifest); err != nil {
		return nil, fmt.Errorf("profile.toml: %w", err)
	}
	if p.SchemaVersion != SupportedSchemaVersion {
		return nil, fmt.Errorf("profile %q: schema_version %d unsupported (wants %d); upgrade pkms",
			p.Name, p.SchemaVersion, SupportedSchemaVersion)
	}
	if p.Name == "" {
		return nil, fmt.Errorf("profile has no name")
	}
	if err := p.compileSchemas(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Profile) compileSchemas() error {
	c := jsonschema.NewCompiler()
	for _, t := range p.Types {
		if t.Schema == "" {
			continue
		}
		raw, err := fs.ReadFile(p.fsys, t.Schema)
		if err != nil {
			return fmt.Errorf("type %q: %w", t.Name, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("type %q schema: %w", t.Name, err)
		}
		url := fmt.Sprintf("pkms://profile/%s/%s/v1", p.Name, t.Name)
		if err := c.AddResource(url, doc); err != nil {
			return fmt.Errorf("type %q schema: %w", t.Name, err)
		}
		sch, err := c.Compile(url)
		if err != nil {
			return fmt.Errorf("type %q schema: %w", t.Name, err)
		}
		p.schemas[t.Name] = sch
	}
	return nil
}

// Schema returns the compiled JSON Schema for a note type (nil if none).
func (p *Profile) Schema(typeName string) *jsonschema.Schema { return p.schemas[typeName] }

// Type returns the declared type by name.
func (p *Profile) Type(name string) *Type {
	for i := range p.Types {
		if p.Types[i].Name == name {
			return &p.Types[i]
		}
	}
	return nil
}

// TypeOf classifies a note deterministically: first declared type whose
// scope matches the vault-relative path and whose key trigger (if any)
// fires. Empty string = unclassified.
func (p *Profile) TypeOf(relPath string, fields map[string]any) string {
	for _, t := range p.Types {
		if !matchAny(t.Scope, relPath) {
			continue
		}
		if len(t.RequireAnyKey) > 0 {
			hit := false
			for _, k := range t.RequireAnyKey {
				if _, present := fields[k]; present {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		return t.Name
	}
	return ""
}

func matchAny(globs []string, relPath string) bool {
	for _, g := range globs {
		if ok, err := doublestar.Match(g, relPath); err == nil && ok {
			return true
		}
	}
	return false
}

// LintConfig returns the profile's config table for a rule (may be nil).
func (p *Profile) LintConfig(ruleID string) map[string]any { return p.Lint[ruleID] }

// RootFile reads templates/root/<name> for `pkms init` scaffolding.
func (p *Profile) RootFile(name string) ([]byte, error) {
	return fs.ReadFile(p.fsys, path.Join("templates", "root", name))
}

// Eject copies the profile's data files to destDir for user customization.
func (p *Profile) Eject(destDir string) error {
	return fs.WalkDir(p.fsys, ".", func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dest := path.Join(destDir, fp)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		raw, err := fs.ReadFile(p.fsys, fp)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, raw, 0o644)
	})
}

var pathTemplateFuncs = template.FuncMap{
	// Date helpers take "YYYY-MM-DD" strings (never wall clock — SPEC §4).
	"year":  func(date string) string { return part(date, 0) },
	"month": func(date string) string { return part(date, 1) },
	"day":   func(date string) string { return part(date, 2) },
	// tsname renders an RFC3339 timestamp (or bare date) as a filename-safe
	// stamp matching the clip-processed-filename convention (SPEC §26).
	"tsname": tsname,
	"slug": func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		var b strings.Builder
		lastDash := false
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
				b.WriteRune(r)
				lastDash = false
			case !lastDash:
				b.WriteByte('-')
				lastDash = true
			}
		}
		return strings.Trim(b.String(), "-")
	},
}

func tsname(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.Format("2006-01-02T150405-0700")
	}
	if t, err := time.Parse("2006-01-02", ts); err == nil {
		return t.Format("2006-01-02T150405+0000")
	}
	return ts // schema validation already vetted the field; pass through
}

func part(date string, i int) string {
	parts := strings.SplitN(date, "-", 3)
	if i < len(parts) {
		return parts[i]
	}
	return ""
}

// RenderPath renders a type's folder + filename templates over validated
// frontmatter fields and confines the result to the vault (SPEC §4, §14).
func (p *Profile) RenderPath(typeName string, fields map[string]any) (folder, filename string, err error) {
	t := p.Type(typeName)
	if t == nil {
		return "", "", fmt.Errorf("unknown note type %q", typeName)
	}
	if t.Folder == "" || t.Filename == "" {
		return "", "", fmt.Errorf("type %q declares no placement templates", typeName)
	}
	folder, err = renderConfined(t.Folder, fields)
	if err != nil {
		return "", "", fmt.Errorf("type %q folder: %w", typeName, err)
	}
	filename, err = renderConfined(t.Filename, fields)
	if err != nil {
		return "", "", fmt.Errorf("type %q filename: %w", typeName, err)
	}
	filename = sanitizeFilename(filename)
	if strings.Contains(filename, "/") {
		return "", "", fmt.Errorf("type %q filename rendered a path separator: %q", typeName, filename)
	}
	if filename == "" {
		return "", "", fmt.Errorf("type %q filename rendered empty after sanitizing", typeName)
	}
	return folder, filename, nil
}

// maxFilenameBytes bounds a rendered basename well under the common 255-byte
// filesystem limit (room for the " N.md" collision suffix); a pathological
// title truncates to a valid note instead of failing the write.
const maxFilenameBytes = 180

// sanitizeFilename replaces the characters the vault forbids in basenames
// (lint filename-safe-chars; SPEC §26) — remote titles are hostile input
// and must never smuggle separators or wikilink syntax into paths — and
// bounds the length so an overlong title can't blow the filesystem limit.
func sanitizeFilename(name string) string {
	return sanitizeChars(name, maxFilenameBytes)
}

func sanitizeChars(name string, maxBytes int) string {
	var b strings.Builder
	for _, r := range name {
		if b.Len() >= maxBytes {
			break
		}
		switch {
		case r < 0x20 || r == 0x7f:
			// drop control chars
		case strings.ContainsRune(`/\#|[]^:`, r):
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// SanitizeAssetFilename applies the sanitizeFilename character rules to a
// stored attachment's basename with EXTENSION-PRESERVING truncation (SPEC
// §31.2): an overlong hostile name must lose stem bytes, never the
// extension Obsidian and the OS dispatch on. An implausibly long
// "extension" (> 16 bytes) is treated as no extension at all.
func SanitizeAssetFilename(name string) string {
	ext := path.Ext(name)
	if len(ext) > 16 {
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	ext = sanitizeChars(ext, len(ext))
	budget := maxFilenameBytes - len(ext)
	if budget < 1 {
		budget = 1
	}
	return sanitizeChars(stem, budget) + ext
}

func renderConfined(tmpl string, fields map[string]any) (string, error) {
	t, err := template.New("p").Funcs(pathTemplateFuncs).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, fields); err != nil {
		return "", err
	}
	out := buf.String()
	clean := path.Clean(out)
	if clean == "" || clean == "." || path.IsAbs(clean) || clean == ".." ||
		strings.HasPrefix(clean, "../") || strings.Contains(out, "\x00") {
		return "", fmt.Errorf("rendered path %q escapes the vault", out)
	}
	return clean, nil
}
