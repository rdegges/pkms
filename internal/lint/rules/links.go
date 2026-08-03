package rules

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/vault"
)

func init() {
	lint.Register("wikilink-resolves", func(cfg map[string]any) (any, error) {
		return wikilinkResolves{}, nil
	})
	lint.Register("wikilink-ambiguous", func(cfg map[string]any) (any, error) {
		return wikilinkAmbiguous{}, nil
	})
	lint.Register("duplicate-basename", func(cfg map[string]any) (any, error) {
		return duplicateBasename{}, nil
	})
	lint.Register("no-broken-embed", func(cfg map[string]any) (any, error) {
		return brokenEmbed{}, nil
	})
	lint.Register("attendee-links-resolve-to-people", func(cfg map[string]any) (any, error) {
		return listResolvesTo{
			key: "attendees", types: []string{"meeting"},
			folders: []string{cfgString(cfg, "folder", "People/")},
			sev:     lint.Error, what: "attendee",
		}, nil
	})
	lint.Register("related-people-resolve-to-people", func(cfg map[string]any) (any, error) {
		return listResolvesTo{
			key: "related_people", types: nil, // any note carrying the key
			folders: []string{cfgString(cfg, "folder", "People/")},
			sev:     lint.Error, what: "related person",
		}, nil
	})
	lint.Register("related-projects-resolve", func(cfg map[string]any) (any, error) {
		folders := cfgStrings(cfg, "folders")
		if len(folders) == 0 {
			folders = []string{"Projects/", "Archive/"}
		}
		return relatedProjects{folders: folders}, nil
	})
	lint.Register("person-meeting-history-links-resolve", func(cfg map[string]any) (any, error) {
		return meetingHistoryLinks{}, nil
	})
	lint.Register("orphan-notes", func(cfg map[string]any) (any, error) {
		scopes := cfgStrings(cfg, "scopes")
		if len(scopes) == 0 {
			return nil, nil
		}
		return orphanNotes{scopes: scopes}, nil
	})
}

// ---- wikilink-resolves ------------------------------------------------------

type wikilinkResolves struct{}

func (wikilinkResolves) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	var out []lint.Finding
	for _, l := range n.Links {
		if l.Embed {
			continue // no-broken-embed owns embeds
		}
		matches := ctx.Ix.Resolve(n.RelPath, l)
		if len(matches) == 0 {
			fixable := singleRepairCandidate(ctx, l) != ""
			out = append(out, finding(lint.Error, n.RelPath, l.Line, fixable,
				"broken link [[%s]]: no note or alias matches", l.Target))
			continue
		}
		// Anchor sub-check (warning — Obsidian tolerates it).
		if l.Fragment != "" && l.Kind == vault.KindWikilink {
			if target := ctx.Ix.Notes[matches[0]]; target != nil {
				if strings.HasPrefix(l.Fragment, "^") {
					if !target.BlockIDs[strings.TrimPrefix(l.Fragment, "^")] {
						out = append(out, finding(lint.Warning, n.RelPath, l.Line, false,
							"block anchor #%s not found in %s", l.Fragment, matches[0]))
					}
				} else if !target.HasHeading(l.Fragment) {
					out = append(out, finding(lint.Warning, n.RelPath, l.Line, false,
						"heading anchor #%s not found in %s", l.Fragment, matches[0]))
				}
			}
		}
	}
	return out
}

// singleRepairCandidate finds the unique basename within Levenshtein
// distance 1 of the broken target (docs/LINT-RULES.md single-repair rule).
// Distance 1 only: at distance 2 the repair starts guessing between real
// distinct names (observed: [[Jamie Cairns]] "repaired" to the different
// real person James Cairns). Targets or candidates containing wikilink
// syntax chars are never auto-repaired: rewriting them cannot round-trip
// through [[...]] parsing (real-world case: a filename like "Janet [C].md"
// — the fix would corrupt the link a little more on every run).
func singleRepairCandidate(ctx *lint.Context, l vault.Link) string {
	if l.Kind != vault.KindWikilink || strings.Contains(l.Target, "/") || l.Target == "" ||
		strings.ContainsAny(l.Target, "[]|#") {
		return ""
	}
	target := strings.ToLower(l.Target)
	var candidates []string
	seen := map[string]bool{}
	for _, p := range ctx.Ix.NotePaths() {
		base := ctx.Ix.Notes[p].Basename()
		if seen[base] {
			continue
		}
		seen[base] = true
		if strings.ContainsAny(base, "[]|#") {
			continue
		}
		if levenshtein(target, strings.ToLower(base)) <= 1 {
			candidates = append(candidates, base)
		}
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	return ""
}

var brokenLinkMsgRe = regexp.MustCompile(`^broken link \[\[(.+)\]\]: no note or alias matches$`)

func (wikilinkResolves) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	m := brokenLinkMsgRe.FindStringSubmatch(f.Message)
	if m == nil {
		return nil, nil
	}
	oldTarget := m[1]
	repl := singleRepairCandidate(ctx, vault.Link{Target: oldTarget, Kind: vault.KindWikilink})
	if repl == "" || repl == oldTarget {
		return nil, nil
	}
	line, ok := getLine(n.Src, f.Line)
	if !ok {
		return nil, nil
	}
	old := []byte("[[" + oldTarget)
	if !strings.Contains(string(line), string(old)) {
		return nil, nil
	}
	newLine := strings.Replace(string(line), string(old), "[["+repl, 1)
	newSrc, ok := replaceLine(n.Src, f.Line, []byte(newLine))
	if !ok {
		return nil, nil
	}
	return &lint.FixResult{NewSrc: newSrc}, nil
}

// ---- wikilink-ambiguous / duplicate-basename -----------------------------------

type wikilinkAmbiguous struct{}

func (wikilinkAmbiguous) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	var out []lint.Finding
	for _, l := range n.Links {
		if l.Kind != vault.KindWikilink || strings.Contains(l.Target, "/") || l.Target == "" {
			continue
		}
		if matches := ctx.Ix.Resolve(n.RelPath, l); len(matches) > 1 {
			sorted := append([]string(nil), matches...)
			sort.Strings(sorted)
			out = append(out, finding(lint.Warning, n.RelPath, l.Line, false,
				"[[%s]] is ambiguous: matches %s", l.Target, strings.Join(sorted, ", ")))
		}
	}
	return out
}

type duplicateBasename struct{}

func (duplicateBasename) CheckVault(ctx *lint.Context) []lint.Finding {
	var out []lint.Finding
	dups := ctx.Ix.DuplicateBasenames()
	var keys []string
	for k := range dups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, p := range dups[k] {
			out = append(out, finding(lint.Warning, p, 0, false,
				"basename shared by %d notes; bare [[%s]] links are ambiguous",
				len(dups[k]), strings.TrimSuffix(path.Base(p), ".md")))
		}
	}
	return out
}

// ---- no-broken-embed --------------------------------------------------------------

type brokenEmbed struct{}

func (brokenEmbed) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	var out []lint.Finding
	for _, l := range n.Links {
		if !l.Embed {
			continue
		}
		if len(ctx.Ix.Resolve(n.RelPath, l)) == 0 {
			out = append(out, finding(lint.Error, n.RelPath, l.Line, false,
				"broken embed ![[%s]]: no file matches", l.Target))
		}
	}
	return out
}

// ---- attendee / related_people (shared shape) ---------------------------------------

type listResolvesTo struct {
	key     string
	types   []string // empty = any note that has the key
	folders []string
	sev     lint.Severity
	what    string
}

func (r listResolvesTo) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if !hasParsedFM(n) {
		return nil
	}
	if len(r.types) > 0 && !contains(r.types, typedNote(ctx, n)) {
		return nil
	}
	entries, _, ok := n.FM.StringList(r.key)
	if !ok {
		return nil
	}
	var out []lint.Finding
	for _, e := range entries {
		target, _, _, _, isLink := vault.ParseWikilinkString(e)
		if !isLink {
			continue // form is another rule's problem
		}
		matches := ctx.Ix.Resolve(n.RelPath, vault.Link{Target: target, Kind: vault.KindWikilink})
		if len(matches) == 0 {
			out = append(out, finding(r.sev, n.RelPath, n.FM.Lines[r.key], false,
				"%s [[%s]] does not resolve to any note", r.what, target))
			continue
		}
		if !anyUnderFolders(matches, r.folders) {
			out = append(out, finding(r.sev, n.RelPath, n.FM.Lines[r.key], false,
				"%s [[%s]] must resolve under %s (resolves to %s)",
				r.what, target, strings.Join(r.folders, " or "), matches[0]))
		}
	}
	return out
}

func anyUnderFolders(paths, folders []string) bool {
	for _, p := range paths {
		for _, f := range folders {
			if strings.HasPrefix(p, f) {
				return true
			}
		}
	}
	return false
}

// ---- related-projects-resolve ---------------------------------------------------------

type relatedProjects struct{ folders []string }

func (r relatedProjects) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if !hasParsedFM(n) {
		return nil
	}
	var out []lint.Finding
	for _, key := range []string{"related_projects", "related"} {
		if key == "related" && typedNote(ctx, n) != "project" {
			continue
		}
		entries, _, ok := n.FM.StringList(key)
		if !ok {
			continue
		}
		for _, e := range entries {
			target, _, _, _, isLink := vault.ParseWikilinkString(e)
			if !isLink {
				continue
			}
			matches := ctx.Ix.Resolve(n.RelPath, vault.Link{Target: target, Kind: vault.KindWikilink})
			if len(matches) == 0 {
				out = append(out, finding(lint.Warning, n.RelPath, n.FM.Lines[key], false,
					"related project [[%s]] does not resolve to any note", target))
			} else if !anyUnderFolders(matches, r.folders) {
				out = append(out, finding(lint.Warning, n.RelPath, n.FM.Lines[key], false,
					"related project [[%s]] must resolve under %s (resolves to %s)",
					target, strings.Join(r.folders, " or "), matches[0]))
			}
		}
	}
	return out
}

// ---- person-meeting-history-links-resolve ------------------------------------------------

var historyBulletRe = regexp.MustCompile(`^- \*\*(\d{4}-\d{2}-\d{2})\*\* — \[\[([^\]|#]+)\]\] — `)

type meetingHistoryLinks struct{}

func (meetingHistoryLinks) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if typedNote(ctx, n) != "person" {
		return nil
	}
	var out []lint.Finding
	for lineNo, text := range sectionLines(n, "Meeting History") {
		m := historyBulletRe.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		date, target := m[1], m[2]
		if strings.Contains(target, "/") {
			// Path-qualified: date consistency is checkable directly.
			if !strings.Contains(target, strings.ReplaceAll(date, "-", "/")) {
				out = append(out, finding(lint.Warning, n.RelPath, lineNo, false,
					"[[%s]] does not live under the bullet's date %s", target, date))
			}
			continue
		}
		if dateQualifiedMeeting(ctx, target, date) == "" {
			fixable := false
			if ms := meetingsOnDate(ctx, target, date); len(ms) == 1 {
				fixable = true
			}
			out = append(out, finding(lint.Warning, n.RelPath, lineNo, fixable,
				"[[%s]] does not resolve to a meeting note dated %s", target, date))
		}
	}
	return out
}

// sectionLines yields (1-based line, text) for body lines inside the named
// H2 section.
func sectionLines(n *vault.Note, section string) map[int]string {
	out := map[int]string{}
	lines := srcLines(n.Src)
	in := false
	for i, l := range lines {
		text := strings.TrimSuffix(string(l), "\n")
		if strings.HasPrefix(text, "## ") {
			// Prefix match: real headings carry suffix annotations.
			in = strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(text, "## ")), section)
			continue
		}
		if in {
			out[i+1] = text
		}
	}
	return out
}

// dateQualifiedMeeting returns the resolved path when the target resolves
// to a meeting under the given date, "" otherwise.
func dateQualifiedMeeting(ctx *lint.Context, target, date string) string {
	datePath := strings.ReplaceAll(date, "-", "/")
	for _, p := range ctx.Ix.Resolve("", vault.Link{Target: target, Kind: vault.KindWikilink}) {
		if strings.Contains(p, "/"+datePath+"/") {
			return p
		}
	}
	return ""
}

// meetingsOnDate finds meeting notes with the exact basename on the date.
func meetingsOnDate(ctx *lint.Context, base, date string) []string {
	datePath := strings.ReplaceAll(date, "-", "/")
	var out []string
	for _, p := range ctx.Ix.NotePaths() {
		if strings.Contains(p, "/"+datePath+"/") && strings.TrimSuffix(path.Base(p), ".md") == base {
			out = append(out, p)
		}
	}
	return out
}

var historyMsgRe = regexp.MustCompile(`^\[\[(.+)\]\] does not resolve to a meeting note dated (\d{4}-\d{2}-\d{2})$`)

func (meetingHistoryLinks) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	m := historyMsgRe.FindStringSubmatch(f.Message)
	if m == nil {
		return nil, nil
	}
	target, date := m[1], m[2]
	ms := meetingsOnDate(ctx, target, date)
	if len(ms) != 1 {
		return nil, nil
	}
	qualified := strings.TrimSuffix(ms[0], ".md")
	line, ok := getLine(n.Src, f.Line)
	if !ok {
		return nil, nil
	}
	newLine := strings.Replace(string(line),
		fmt.Sprintf("[[%s]]", target), fmt.Sprintf("[[%s]]", qualified), 1)
	newSrc, ok := replaceLine(n.Src, f.Line, []byte(newLine))
	if !ok {
		return nil, nil
	}
	return &lint.FixResult{NewSrc: newSrc}, nil
}

// ---- orphan-notes -----------------------------------------------------------------------------

type orphanNotes struct{ scopes []string }

func (r orphanNotes) CheckVault(ctx *lint.Context) []lint.Finding {
	var out []lint.Finding
	for _, p := range ctx.Ix.NotePaths() {
		if !matchAnyGlob(r.scopes, p) {
			continue
		}
		inbound := 0
		for _, ref := range ctx.Ix.Backlinks[p] {
			if ref.Source != p {
				inbound++
			}
		}
		if inbound == 0 {
			out = append(out, finding(lint.Warning, p, 0, false,
				"orphan note: no inbound links and no catalog entry"))
		}
	}
	return out
}
