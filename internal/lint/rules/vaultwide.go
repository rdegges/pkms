package rules

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/vault"
)

func init() {
	lint.Register("action-items-count-drift", func(cfg map[string]any) (any, error) {
		file, err := lint.CfgString(cfg, "file", "")
		if err != nil {
			return nil, err
		}
		key, err := lint.CfgString(cfg, "key", "total_items")
		if err != nil {
			return nil, err
		}
		if file == "" {
			return nil, nil
		}
		return countDrift{file: file, key: key, mode: "checkboxes"}, nil
	})
	lint.Register("recipes-count-drift", func(cfg map[string]any) (any, error) {
		file, err := lint.CfgString(cfg, "file", "")
		if err != nil {
			return nil, err
		}
		counts, err := lint.CfgString(cfg, "counts", "")
		if err != nil {
			return nil, err
		}
		key, err := lint.CfgString(cfg, "key", "recipe_count")
		if err != nil {
			return nil, err
		}
		if file == "" || counts == "" {
			return nil, nil
		}
		if err := validGlobs("counts", []string{counts}); err != nil {
			return nil, err
		}
		return countDrift{file: file, key: key, mode: "files", glob: counts}, nil
	})
	lint.Register("recipes-index-links-complete", func(cfg map[string]any) (any, error) {
		file, lists, err := cfgIndexPair(cfg)
		if err != nil || file == "" {
			return nil, err
		}
		return indexComplete{file: file, lists: lists, sev: lint.Error, reverse: true}, nil
	})
	lint.Register("resources-cataloged-in-index", func(cfg map[string]any) (any, error) {
		file, lists, err := cfgIndexPair(cfg)
		if err != nil || file == "" {
			return nil, err
		}
		return indexComplete{file: file, lists: lists, sev: lint.Warning}, nil
	})
	lint.Register("projects-linked-from-master", func(cfg map[string]any) (any, error) {
		file, lists, err := cfgIndexPair(cfg)
		if err != nil || file == "" {
			return nil, err
		}
		return indexComplete{file: file, lists: lists, sev: lint.Warning}, nil
	})
	lint.Register("index-no-inventory", func(cfg map[string]any) (any, error) {
		file, err := lint.CfgString(cfg, "file", "")
		if err != nil {
			return nil, err
		}
		prefixes, err := lint.CfgStrings(cfg, "forbidden_prefixes")
		if err != nil {
			return nil, err
		}
		if file == "" || len(prefixes) == 0 {
			return nil, nil
		}
		return indexNoInventory{file: file, prefixes: prefixes}, nil
	})
	lint.Register("log-entry-format", func(cfg map[string]any) (any, error) {
		file, err := lint.CfgString(cfg, "file", "")
		if err != nil || file == "" {
			return nil, err
		}
		return logFormat{file: file}, nil
	})
	lint.Register("log-action-vocab", func(cfg map[string]any) (any, error) {
		file, err := lint.CfgString(cfg, "file", "")
		if err != nil {
			return nil, err
		}
		allow, err := lint.CfgStrings(cfg, "allowlist")
		if err != nil {
			return nil, err
		}
		if file == "" || len(allow) == 0 {
			return nil, nil
		}
		return logVocab{file: file, allow: allow}, nil
	})
	lint.Register("log-newest-first", func(cfg map[string]any) (any, error) {
		file, err := lint.CfgString(cfg, "file", "")
		if err != nil || file == "" {
			return nil, err
		}
		return logNewestFirst{file: file}, nil
	})
	lint.Register("log-entry-bullets-flat", func(cfg map[string]any) (any, error) {
		enabled, err := lint.CfgBool(cfg, "enabled", false)
		if err != nil {
			return nil, err
		}
		if !enabled { // documented vs real practice conflict
			return nil, nil
		}
		file, err := lint.CfgString(cfg, "file", "log.md")
		if err != nil {
			return nil, err
		}
		return logFlatBullets{file: file}, nil
	})
	lint.Register("now-line-cap", func(cfg map[string]any) (any, error) {
		file, err := lint.CfgString(cfg, "file", "")
		if err != nil {
			return nil, err
		}
		warnAt, err := lint.CfgInt(cfg, "warn_at", 60)
		if err != nil {
			return nil, err
		}
		errorAt, err := lint.CfgInt(cfg, "error_at", 80)
		if err != nil {
			return nil, err
		}
		if file == "" {
			return nil, nil
		}
		return nowLineCap{file: file, warnAt: warnAt, errorAt: errorAt}, nil
	})
	lint.Register("now-fixed-sections", func(cfg map[string]any) (any, error) {
		file, err := lint.CfgString(cfg, "file", "")
		if err != nil {
			return nil, err
		}
		sections, err := lint.CfgStrings(cfg, "sections")
		if err != nil {
			return nil, err
		}
		if file == "" || len(sections) == 0 {
			return nil, nil
		}
		return nowSections{file: file, sections: sections}, nil
	})
	lint.Register("now-no-sync-sections", func(cfg map[string]any) (any, error) {
		file, err := lint.CfgString(cfg, "file", "")
		if err != nil {
			return nil, err
		}
		res, err := cfgRegexps(cfg, "patterns")
		if err != nil {
			return nil, err
		}
		if file == "" || len(res) == 0 {
			return nil, nil
		}
		return nowNoSync{file: file, patterns: res}, nil
	})
	lint.Register("now-active-projects-shape", func(cfg map[string]any) (any, error) {
		file, err := lint.CfgString(cfg, "file", "")
		if err != nil {
			return nil, err
		}
		section, err := lint.CfgString(cfg, "section", "Active Projects")
		if err != nil {
			return nil, err
		}
		minB, err := lint.CfgInt(cfg, "min_bullets", 5)
		if err != nil {
			return nil, err
		}
		maxB, err := lint.CfgInt(cfg, "max_bullets", 8)
		if err != nil {
			return nil, err
		}
		if file == "" {
			return nil, nil
		}
		return nowProjects{file: file, section: section, min: minB, max: maxB}, nil
	})
}

// cfgIndexPair reads the file/lists pair the index rules share and
// validates the lists glob. file == "" (with lists unset too) means
// unconfigured.
func cfgIndexPair(cfg map[string]any) (file, lists string, err error) {
	file, err = lint.CfgString(cfg, "file", "")
	if err != nil {
		return "", "", err
	}
	lists, err = lint.CfgString(cfg, "lists", "")
	if err != nil {
		return "", "", err
	}
	if file == "" || lists == "" {
		return "", "", nil
	}
	if err := validGlobs("lists", []string{lists}); err != nil {
		return "", "", err
	}
	return file, lists, nil
}

// ---- count drift (Action Items / Recipes) -----------------------------------

var checkboxRe = regexp.MustCompile(`(?m)^\s*- \[[ xX]\] `)

type countDrift struct {
	file, key, mode, glob string
}

func (r countDrift) CheckVault(ctx *lint.Context) []lint.Finding {
	n := ctx.Ix.Notes[r.file]
	if n == nil || !hasParsedFM(n) {
		return nil
	}
	declared, ok := n.FM.Fields[r.key].(int64)
	if !ok {
		return nil // wrong type is a schema finding
	}
	actual := r.actual(ctx)
	if int(declared) == actual {
		return nil
	}
	return []lint.Finding{finding(lint.Error, r.file, n.FM.Lines[r.key], true,
		"%s: %d declared but %d counted", r.key, declared, actual)}
}

func (r countDrift) actual(ctx *lint.Context) int {
	switch r.mode {
	case "checkboxes":
		n := ctx.Ix.Notes[r.file]
		return len(checkboxRe.FindAll(n.Body, -1))
	case "files":
		count := 0
		for _, p := range ctx.Ix.NotePaths() {
			if p != r.file && matchAnyGlob([]string{r.glob}, p) {
				count++
			}
		}
		return count
	}
	return 0
}

func (r countDrift) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	if !hasParsedFM(n) {
		return nil, nil
	}
	lineNo := n.FM.Lines[r.key]
	line, ok := getLine(n.Src, lineNo)
	if !ok {
		return nil, nil
	}
	keyRe := regexp.MustCompile(`^(\s*` + regexp.QuoteMeta(r.key) + `:\s*)\d+\s*$`)
	if !keyRe.Match(line) {
		return nil, nil
	}
	newLine := keyRe.ReplaceAll(line, []byte(fmt.Sprintf("${1}%d", r.actual(ctx))))
	newSrc, ok := replaceLine(n.Src, lineNo, newLine)
	if !ok {
		return nil, nil
	}
	return &lint.FixResult{NewSrc: newSrc}, nil
}

// ---- index completeness (master catalogs) -------------------------------------

type indexComplete struct {
	file    string
	lists   string
	sev     lint.Severity
	reverse bool // also: every index link must resolve (recipes)
}

func (r indexComplete) CheckVault(ctx *lint.Context) []lint.Finding {
	var inScope []string
	for _, p := range ctx.Ix.NotePaths() {
		if p != r.file && matchAnyGlob([]string{r.lists}, p) {
			inScope = append(inScope, p)
		}
	}
	idx := ctx.Ix.Notes[r.file]
	if idx == nil {
		if len(inScope) == 0 {
			return nil // nothing to catalog and no index: fine (fresh vault)
		}
		return []lint.Finding{finding(r.sev, r.file, 0, false,
			"index file missing (%d note(s) to catalog)", len(inScope))}
	}
	// Resolve every link in the index once.
	linked := map[string]bool{}
	var out []lint.Finding
	for _, l := range idx.Links {
		matches := ctx.Ix.Resolve(idx.RelPath, l)
		for _, m := range matches {
			linked[m] = true
		}
		if r.reverse && len(matches) == 0 && l.Kind == vault.KindWikilink {
			out = append(out, finding(r.sev, r.file, l.Line, false,
				"index links [[%s]] but no such note exists", l.Target))
		}
	}
	for _, p := range inScope {
		if !linked[p] {
			out = append(out, finding(r.sev, p, 0, false,
				"not cataloged in %s", r.file))
		}
	}
	return out
}

// ---- index-no-inventory ---------------------------------------------------------

type indexNoInventory struct {
	file     string
	prefixes []string
}

func (r indexNoInventory) CheckVault(ctx *lint.Context) []lint.Finding {
	idx := ctx.Ix.Notes[r.file]
	if idx == nil {
		return nil
	}
	var out []lint.Finding
	for _, l := range idx.Links {
		for _, m := range ctx.Ix.Resolve(idx.RelPath, l) {
			for _, pre := range r.prefixes {
				if strings.HasPrefix(m, pre) {
					out = append(out, finding(lint.Warning, r.file, l.Line, false,
						"%s must not inventory %s (links [[%s]])", r.file, pre, l.Target))
				}
			}
		}
	}
	return out
}

// ---- log.md rules ------------------------------------------------------------------

var logHeaderRe = regexp.MustCompile(`^## \[(\d{4}-\d{2}-\d{2})\] ([a-z][a-z-]*) \| .+$`)

type logEntry struct {
	line   int
	date   string
	action string
	body   []string
}

func parseLog(n *vault.Note) (entries []logEntry, badHeaders []int) {
	lines := srcLines(n.Src)
	var cur *logEntry
	for i, l := range lines {
		text := strings.TrimSuffix(string(l), "\n")
		if strings.HasPrefix(text, "## ") {
			if cur != nil {
				entries = append(entries, *cur)
			}
			m := logHeaderRe.FindStringSubmatch(text)
			if m == nil || !isISODate(m[1]) {
				badHeaders = append(badHeaders, i+1)
				cur = nil
				continue
			}
			cur = &logEntry{line: i + 1, date: m[1], action: m[2]}
			continue
		}
		if cur != nil {
			cur.body = append(cur.body, text)
		}
	}
	if cur != nil {
		entries = append(entries, *cur)
	}
	return entries, badHeaders
}

type logFormat struct{ file string }

func (r logFormat) CheckVault(ctx *lint.Context) []lint.Finding {
	n := ctx.Ix.Notes[r.file]
	if n == nil {
		return nil
	}
	entries, bad := parseLog(n)
	var out []lint.Finding
	for _, line := range bad {
		out = append(out, finding(lint.Error, r.file, line, false,
			"log entry header must be '## [YYYY-MM-DD] action | summary'"))
	}
	for _, e := range entries {
		hasBullet := false
		for _, b := range e.body {
			if strings.HasPrefix(strings.TrimSpace(b), "- ") {
				hasBullet = true
				break
			}
		}
		if !hasBullet {
			out = append(out, finding(lint.Error, r.file, e.line, false,
				"log entry has no '- ' bullets"))
		}
	}
	return out
}

type logVocab struct {
	file  string
	allow []string
}

func (r logVocab) CheckVault(ctx *lint.Context) []lint.Finding {
	n := ctx.Ix.Notes[r.file]
	if n == nil {
		return nil
	}
	entries, _ := parseLog(n)
	var out []lint.Finding
	for _, e := range entries {
		if !contains(r.allow, e.action) {
			out = append(out, finding(lint.Warning, r.file, e.line, false,
				"log action %q is not in the allowlist", e.action))
		}
	}
	return out
}

type logNewestFirst struct{ file string }

func (r logNewestFirst) CheckVault(ctx *lint.Context) []lint.Finding {
	n := ctx.Ix.Notes[r.file]
	if n == nil {
		return nil
	}
	entries, _ := parseLog(n)
	var out []lint.Finding
	for i := 1; i < len(entries); i++ {
		if entries[i].date > entries[i-1].date {
			out = append(out, finding(lint.Error, r.file, entries[i].line, false,
				"log entries must be newest-first (%s appears below %s)",
				entries[i].date, entries[i-1].date))
		}
	}
	return out
}

type logFlatBullets struct{ file string }

func (r logFlatBullets) CheckVault(ctx *lint.Context) []lint.Finding {
	n := ctx.Ix.Notes[r.file]
	if n == nil {
		return nil
	}
	entries, _ := parseLog(n)
	var out []lint.Finding
	for _, e := range entries {
		for _, b := range e.body {
			if regexp.MustCompile(`^\s+- `).MatchString(b) {
				out = append(out, finding(lint.Warning, r.file, e.line, false,
					"log entry bullets must be flat (no nesting)"))
				break
			}
		}
	}
	return out
}

// ---- Now.md rules ---------------------------------------------------------------------

type nowLineCap struct {
	file            string
	warnAt, errorAt int
}

func (r nowLineCap) CheckVault(ctx *lint.Context) []lint.Finding {
	n := ctx.Ix.Notes[r.file]
	if n == nil {
		return nil
	}
	count := n.LineCount()
	switch {
	case count > r.errorAt:
		return []lint.Finding{finding(lint.Error, r.file, 0, false,
			"%d lines exceeds the hard cap of %d", count, r.errorAt)}
	case count > r.warnAt:
		return []lint.Finding{finding(lint.Warning, r.file, 0, false,
			"%d lines exceeds the target of %d", count, r.warnAt)}
	}
	return nil
}

func h2s(n *vault.Note) map[int]string {
	out := map[int]string{}
	for i, l := range srcLines(n.Src) {
		text := strings.TrimSuffix(string(l), "\n")
		if strings.HasPrefix(text, "## ") {
			out[i+1] = strings.TrimSpace(strings.TrimPrefix(text, "## "))
		}
	}
	return out
}

type nowSections struct {
	file     string
	sections []string
}

func (r nowSections) CheckVault(ctx *lint.Context) []lint.Finding {
	n := ctx.Ix.Notes[r.file]
	if n == nil {
		return nil
	}
	headings := h2s(n)
	var lines []int
	for l := range headings {
		lines = append(lines, l)
	}
	sort.Ints(lines)

	var out []lint.Finding
	rank := 0
	for _, l := range lines {
		h := headings[l]
		matched := -1
		for i := range r.sections {
			if strings.HasPrefix(h, r.sections[i]) {
				matched = i
				break
			}
		}
		if matched < 0 {
			out = append(out, finding(lint.Error, r.file, l, false,
				"unexpected H2 %q (allowed, in order: %s)", h, strings.Join(r.sections, ", ")))
			continue
		}
		if matched < rank {
			out = append(out, finding(lint.Error, r.file, l, false,
				"H2 %q is out of order (expected sequence: %s)", h, strings.Join(r.sections, ", ")))
		} else {
			rank = matched
		}
	}
	return out
}

type nowNoSync struct {
	file     string
	patterns []*regexp.Regexp
}

func (r nowNoSync) CheckVault(ctx *lint.Context) []lint.Finding {
	n := ctx.Ix.Notes[r.file]
	if n == nil {
		return nil
	}
	var out []lint.Finding
	for i, l := range srcLines(n.Src) {
		text := strings.TrimSuffix(string(l), "\n")
		if !strings.HasPrefix(text, "#") {
			continue
		}
		h := strings.TrimSpace(strings.TrimLeft(text, "# "))
		for _, re := range r.patterns {
			if re.MatchString(h) {
				out = append(out, finding(lint.Error, r.file, i+1, false,
					"sync-artifact heading %q is banned; relocate the content", h))
				break
			}
		}
	}
	return out
}

type nowProjects struct {
	file, section string
	min, max      int
}

func (r nowProjects) CheckVault(ctx *lint.Context) []lint.Finding {
	n := ctx.Ix.Notes[r.file]
	if n == nil {
		return nil
	}
	bullets, nested := 0, 0
	first := 0
	for lineNo, text := range sectionLines(n, r.section) {
		if first == 0 || lineNo < first {
			first = lineNo
		}
		if strings.HasPrefix(text, "- ") {
			bullets++
		} else if regexp.MustCompile(`^\s+- `).MatchString(text) {
			nested++
		}
	}
	if first == 0 {
		return nil
	}
	var out []lint.Finding
	if nested > 0 {
		out = append(out, finding(lint.Warning, r.file, first, false,
			"%s bullets must not nest (%d nested)", r.section, nested))
	}
	if bullets < r.min || bullets > r.max {
		out = append(out, finding(lint.Warning, r.file, first, false,
			"%s should have %d-%d bullets (has %d)", r.section, r.min, r.max, bullets))
	}
	return out
}
