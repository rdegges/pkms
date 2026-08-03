package rules

import (
	"bytes"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/vault"
)

func init() {
	lint.Register("root-canonical-only", func(cfg map[string]any) (any, error) {
		files := cfgStrings(cfg, "files")
		if len(files) == 0 {
			return nil, nil
		}
		return rootCanonical{files: files, allow: cfgStrings(cfg, "allowlist")}, nil
	})
	lint.Register("root-file-name-case", func(cfg map[string]any) (any, error) {
		return rootNameCase{}, nil
	})
	lint.Register("top-level-folders-fixed", func(cfg map[string]any) (any, error) {
		dirs := cfgStrings(cfg, "dirs")
		if len(dirs) == 0 {
			return nil, nil
		}
		return topFolders{dirs: dirs, allow: cfgStrings(cfg, "allowlist")}, nil
	})
	lint.Register("domain-split-folders", func(cfg map[string]any) (any, error) {
		splits := map[string][]string{}
		for k, v := range cfg {
			if k == "enabled" || k == "severity" {
				continue
			}
			if list, ok := v.([]any); ok {
				var ss []string
				for _, e := range list {
					if s, ok := e.(string); ok {
						ss = append(ss, s)
					}
				}
				splits[k] = ss
			}
		}
		if len(splits) == 0 {
			return nil, nil
		}
		return domainSplit{splits: splits}, nil
	})
	lint.Register("meeting-filename-format", func(cfg map[string]any) (any, error) {
		return meetingFilename{extra: cfgStrings(cfg, "extra_allowed")}, nil
	})
	lint.Register("meeting-path-valid-date", func(cfg map[string]any) (any, error) {
		return meetingPathDate{}, nil
	})
	lint.Register("clip-processed-filename", func(cfg map[string]any) (any, error) {
		return clipFilename{}, nil
	})
	lint.Register("session-trace-filename", func(cfg map[string]any) (any, error) {
		return traceFilename{}, nil
	})
	lint.Register("filename-safe-chars", func(cfg map[string]any) (any, error) {
		return safeChars{}, nil
	})
	lint.Register("no-per-run-notes", func(cfg map[string]any) (any, error) {
		res, err := cfgRegexps(cfg, "patterns")
		if err != nil {
			return nil, err
		}
		if len(res) == 0 {
			return nil, nil
		}
		return perRunNotes{patterns: res}, nil
	})
	lint.Register("no-drafts-folder", func(cfg map[string]any) (any, error) {
		res, err := cfgRegexps(cfg, "patterns")
		if err != nil {
			return nil, err
		}
		return draftsFolder{dir: cfgString(cfg, "dir", "Drafts"), patterns: res}, nil
	})
	lint.Register("no-junk-files", func(cfg map[string]any) (any, error) {
		pats := cfgStrings(cfg, "patterns")
		if len(pats) == 0 {
			pats = []string{"*.bak", ".DS_Store", "* conflicted copy*", "~$*", "*.tmp"}
		}
		return junkFiles{patterns: pats, underscoreExempt: cfgStrings(cfg, "underscore_exempt")}, nil
	})
	lint.Register("non-markdown-in-note-folders", func(cfg map[string]any) (any, error) {
		scopes := cfgStrings(cfg, "scopes")
		if len(scopes) == 0 {
			return nil, nil
		}
		return nonMDInNotes{scopes: scopes}, nil
	})
	lint.Register("template-placeholders-in-real-notes", func(cfg map[string]any) (any, error) {
		return templatePlaceholders{}, nil
	})
	lint.Register("empty-note", func(cfg map[string]any) (any, error) {
		return emptyNote{}, nil
	})
}

func sortedFiles(ctx *lint.Context) []string {
	out := make([]string, 0, len(ctx.Ix.Files))
	for f := range ctx.Ix.Files {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// ---- root-canonical-only / root-file-name-case ------------------------------

type rootCanonical struct{ files, allow []string }

func (r rootCanonical) CheckVault(ctx *lint.Context) []lint.Finding {
	var out []lint.Finding
	for _, f := range sortedFiles(ctx) {
		if strings.Contains(f, "/") || contains(r.files, f) || contains(r.allow, f) {
			continue
		}
		// Case-variant of a canonical file → root-file-name-case territory.
		if caseVariantOf(f, r.files) != "" {
			continue
		}
		out = append(out, finding(lint.Error, f, 0, false,
			"unexpected file in vault root (canonical set: %s)", strings.Join(r.files, ", ")))
	}
	return out
}

func caseVariantOf(f string, canonical []string) string {
	for _, c := range canonical {
		if strings.EqualFold(f, c) && f != c {
			return c
		}
	}
	return ""
}

type rootNameCase struct{}

func (rootNameCase) CheckVault(ctx *lint.Context) []lint.Finding {
	canonical := cfgStrings(ctx.Prof.LintConfig("root-canonical-only"), "files")
	if len(canonical) == 0 {
		return nil
	}
	var out []lint.Finding
	for _, f := range sortedFiles(ctx) {
		if strings.Contains(f, "/") {
			continue
		}
		if want := caseVariantOf(f, canonical); want != "" {
			out = append(out, finding(lint.Error, f, 0, false,
				"root file must be spelled %q (renames break links; fix manually)", want))
		}
	}
	return out
}

// ---- top-level-folders-fixed --------------------------------------------------

type topFolders struct{ dirs, allow []string }

func (r topFolders) CheckVault(ctx *lint.Context) []lint.Finding {
	var out []lint.Finding
	var tops []string
	for d := range ctx.Ix.Dirs {
		if !strings.Contains(d, "/") {
			tops = append(tops, d)
		}
	}
	sort.Strings(tops)
	for _, d := range tops {
		if contains(r.dirs, d) || contains(r.allow, d) || d == ctx.Prof.Attachments {
			continue
		}
		out = append(out, finding(lint.Error, d, 0, false,
			"unexpected top-level folder (allowed: %s)", strings.Join(r.dirs, ", ")))
	}
	return out
}

// ---- domain-split-folders --------------------------------------------------------

type domainSplit struct{ splits map[string][]string }

func (r domainSplit) CheckVault(ctx *lint.Context) []lint.Finding {
	var out []lint.Finding
	var keys []string
	for k := range r.splits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, top := range keys {
		allowed := r.splits[top]
		for _, d := range sortedDirs(ctx) {
			if path.Dir(d) == top && !contains(allowed, path.Base(d)) {
				out = append(out, finding(lint.Error, d, 0, false,
					"%s/ only allows subfolders %v", top, allowed))
			}
		}
		for _, f := range sortedFiles(ctx) {
			if path.Dir(f) == top && strings.HasSuffix(f, ".md") {
				out = append(out, finding(lint.Error, f, 0, false,
					"notes must live inside a domain split of %s/ (%v)", top, allowed))
			}
		}
	}
	return out
}

func sortedDirs(ctx *lint.Context) []string {
	out := make([]string, 0, len(ctx.Ix.Dirs))
	for d := range ctx.Ix.Dirs {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// ---- meeting-filename-format ------------------------------------------------------

var meetingNameRe = regexp.MustCompile(`^([01][0-9]|2[0-3])[0-5][0-9] - .+\.md$`)
var meetingDayDirRe = regexp.MustCompile(`^Meetings/[^/]+/\d{4}/\d{2}/\d{2}$`)

type meetingFilename struct{ extra []string }

func (r meetingFilename) CheckVault(ctx *lint.Context) []lint.Finding {
	var out []lint.Finding
	for _, f := range sortedFiles(ctx) {
		if !strings.HasSuffix(f, ".md") || !meetingDayDirRe.MatchString(path.Dir(f)) {
			continue
		}
		base := path.Base(f)
		if meetingNameRe.MatchString(base) || contains(r.extra, base) {
			continue
		}
		out = append(out, finding(lint.Error, f, 0, false,
			"meeting filename must be 'HHMM - Title.md' (or one of %v)", r.extra))
	}
	return out
}

// ---- meeting-path-valid-date ---------------------------------------------------------

type meetingPathDate struct{}

func (meetingPathDate) CheckVault(ctx *lint.Context) []lint.Finding {
	var out []lint.Finding
	seen := map[string]bool{}
	for _, p := range append(sortedDirs(ctx), sortedFiles(ctx)...) {
		parts := strings.Split(p, "/")
		if parts[0] != "Meetings" || len(parts) < 3 {
			continue
		}
		// parts: Meetings/<domain>/<YYYY>/<MM>/<DD>/...
		isFile := ctx.Ix.Files[p]
		rest := parts[2:]
		if isFile {
			rest = parts[2 : len(parts)-1]
		}
		if len(rest) > 3 {
			continue // deeper nesting flagged at its own level
		}
		dir := strings.Join(parts[:2+len(rest)], "/")
		if seen[dir] {
			continue
		}
		ok := true
		if len(rest) >= 1 && !regexp.MustCompile(`^\d{4}$`).MatchString(rest[0]) {
			ok = false
		}
		if len(rest) >= 2 && !regexp.MustCompile(`^\d{2}$`).MatchString(rest[1]) {
			ok = false
		}
		if len(rest) == 3 {
			if !regexp.MustCompile(`^\d{2}$`).MatchString(rest[2]) {
				ok = false
			} else if _, err := time.Parse("2006/01/02", strings.Join(rest, "/")); err != nil {
				ok = false
			}
		}
		if !ok {
			seen[dir] = true
			out = append(out, finding(lint.Error, dir, 0, false,
				"meeting paths must be Meetings/<domain>/YYYY/MM/DD (zero-padded, real date)"))
		}
		// Files may not sit at intermediate levels.
		if isFile && len(rest) < 3 {
			out = append(out, finding(lint.Error, p, 0, false,
				"files may not sit at intermediate meeting-path levels"))
		}
	}
	return out
}

// ---- clip-processed-filename -----------------------------------------------------------

var clipNameRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{6}[+-]\d{4} - .+\.md$`)

type clipFilename struct{}

func (clipFilename) CheckVault(ctx *lint.Context) []lint.Finding {
	var out []lint.Finding
	for _, f := range sortedFiles(ctx) {
		dir := path.Dir(f)
		if (dir == "Resources/Clips/Inbox" || dir == "Resources/Clips/Processed") &&
			strings.HasSuffix(f, ".md") && !clipNameRe.MatchString(path.Base(f)) {
			out = append(out, finding(lint.Warning, f, 0, false,
				"clip filename should be 'YYYY-MM-DDTHHMMSS±ZZZZ - Title.md'"))
		}
	}
	return out
}

// ---- session-trace-filename ------------------------------------------------------------

var traceHyphenRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}) - (.+\.md)$`)

type traceFilename struct{}

func (traceFilename) CheckVault(ctx *lint.Context) []lint.Finding {
	var out []lint.Finding
	for _, f := range sortedFiles(ctx) {
		if path.Dir(f) != "Resources/Personal/Session Traces" || !strings.HasSuffix(f, ".md") {
			continue
		}
		base := path.Base(f)
		if base == "_template.md" || traceFilenameRe.MatchString(base) {
			continue
		}
		// Hyphen-instead-of-em-dash is fixable only with no inbound links.
		fixable := traceHyphenRe.MatchString(base) && len(ctx.Ix.Backlinks[f]) == 0
		out = append(out, finding(lint.Error, f, 0, fixable,
			"session trace filename must be 'YYYY-MM-DD — slug.md' (em dash)"))
	}
	return out
}

func (traceFilename) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	base := baseOf(n.RelPath)
	m := traceHyphenRe.FindStringSubmatch(base)
	if m == nil || len(ctx.Ix.Backlinks[n.RelPath]) > 0 {
		return nil, nil
	}
	newBase := m[1] + " — " + m[2]
	return &lint.FixResult{RenameTo: path.Join(path.Dir(n.RelPath), newBase)}, nil
}

// ---- filename-safe-chars ------------------------------------------------------------------

var unsafeChars = `\#|[]^:`

type safeChars struct{}

func (safeChars) CheckVault(ctx *lint.Context) []lint.Finding {
	var out []lint.Finding
	for _, f := range sortedFiles(ctx) {
		if !strings.HasSuffix(f, ".md") {
			continue
		}
		base := path.Base(f)
		if strings.ContainsAny(base, unsafeChars) || strings.ContainsFunc(base, func(r rune) bool { return r < 0x20 }) {
			out = append(out, finding(lint.Error, f, 0, false,
				`filename contains unsafe characters (\ # | [ ] ^ : or control chars)`))
		}
	}
	return out
}

// ---- no-per-run-notes ----------------------------------------------------------------------

type perRunNotes struct{ patterns []*regexp.Regexp }

func (r perRunNotes) CheckVault(ctx *lint.Context) []lint.Finding {
	var out []lint.Finding
	for _, f := range sortedFiles(ctx) {
		if !strings.HasSuffix(f, ".md") {
			continue
		}
		base := strings.TrimSuffix(path.Base(f), ".md")
		for _, re := range r.patterns {
			if re.MatchString(base) {
				out = append(out, finding(lint.Error, f, 0, false,
					"per-run artifact notes are banned (pattern %q); log to log.md instead", re.String()))
				break
			}
		}
	}
	return out
}

// ---- no-drafts-folder -------------------------------------------------------------------------

type draftsFolder struct {
	dir      string
	patterns []*regexp.Regexp
}

func (r draftsFolder) CheckVault(ctx *lint.Context) []lint.Finding {
	var out []lint.Finding
	for _, d := range sortedDirs(ctx) {
		if path.Base(d) == r.dir {
			out = append(out, finding(lint.Error, d, 0, false,
				"the %s/ folder is gone and is not coming back", r.dir))
		}
	}
	for _, f := range sortedFiles(ctx) {
		base := path.Base(f)
		for _, re := range r.patterns {
			if re.MatchString(base) {
				out = append(out, finding(lint.Error, f, 0, false,
					"message-draft notes are banned (pattern %q)", re.String()))
				break
			}
		}
	}
	return out
}

// ---- no-junk-files ------------------------------------------------------------------------------

type junkFiles struct {
	patterns         []string
	underscoreExempt []string
}

func (r junkFiles) CheckVault(ctx *lint.Context) []lint.Finding {
	var out []lint.Finding
	att := ctx.Prof.Attachments
	for _, f := range sortedFiles(ctx) {
		if att != "" && (f == att || strings.HasPrefix(f, att+"/")) {
			continue
		}
		base := path.Base(f)
		matched := false
		for _, p := range r.patterns {
			if ok, _ := path.Match(p, base); ok {
				matched = true
				break
			}
		}
		if !matched && strings.HasPrefix(base, "_") && !contains(r.underscoreExempt, base) {
			matched = true
		}
		if matched {
			out = append(out, finding(lint.Warning, f, 0, false,
				"junk/scratch file (deletion is never auto-fixed; clean up manually)"))
		}
	}
	return out
}

// ---- non-markdown-in-note-folders -----------------------------------------------------------------

type nonMDInNotes struct{ scopes []string }

func (r nonMDInNotes) CheckVault(ctx *lint.Context) []lint.Finding {
	var out []lint.Finding
	for _, f := range sortedFiles(ctx) {
		if strings.HasSuffix(f, ".md") || !matchAnyGlob(r.scopes, f) {
			continue
		}
		out = append(out, finding(lint.Warning, f, 0, false,
			"only markdown belongs here"))
	}
	return out
}

// ---- template-placeholders-in-real-notes ------------------------------------------------------------

var placeholderRe = regexp.MustCompile(`\{\{\s*(date|slug|[a-z_]+)\s*\}\}`)

type templatePlaceholders struct{}

func (templatePlaceholders) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	base := baseOf(n.RelPath)
	if base == "_template.md" || strings.HasSuffix(base, "_template.md") {
		return nil
	}
	loc := placeholderRe.FindIndex(n.Src)
	if loc == nil {
		return nil
	}
	token := string(n.Src[loc[0]:loc[1]])
	fixable := (strings.Contains(token, "date") || strings.Contains(token, "slug")) &&
		traceFilenameRe.MatchString(base)
	return []lint.Finding{finding(lint.Error, n.RelPath, n.LineOf(loc[0]), fixable,
		"unfilled template placeholder %s", token)}
}

func (templatePlaceholders) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	m := traceFilenameRe.FindStringSubmatch(baseOf(n.RelPath))
	if m == nil {
		return nil, nil
	}
	src := regexp.MustCompile(`\{\{\s*date\s*\}\}`).ReplaceAll(n.Src, []byte(m[1]))
	src = regexp.MustCompile(`\{\{\s*slug\s*\}\}`).ReplaceAll(src, []byte(m[2]))
	if bytes.Equal(src, n.Src) {
		return nil, nil
	}
	return &lint.FixResult{NewSrc: src}, nil
}

// ---- empty-note ----------------------------------------------------------------------------------------

type emptyNote struct{}

func (emptyNote) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if len(bytes.TrimSpace(n.Body)) > 0 {
		return nil
	}
	if n.FM != nil && n.FM.Unclosed {
		return nil // unclosed frontmatter swallowed the body; that rule fires
	}
	return []lint.Finding{finding(lint.Warning, n.RelPath, 0, false,
		"note has no body content")}
}
