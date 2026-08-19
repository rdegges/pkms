package rules

import (
	"regexp"
	"sort"
	"strings"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/vault"
)

func init() {
	lint.Register("person-required-sections", func(cfg map[string]any) (any, error) {
		sections, err := lint.CfgStrings(cfg, "sections")
		if err != nil {
			return nil, err
		}
		if len(sections) == 0 {
			return nil, nil
		}
		return requiredSections{typ: "person", sections: sections}, nil
	})
	lint.Register("meeting-required-sections", func(cfg map[string]any) (any, error) {
		sections, err := lint.CfgStrings(cfg, "sections")
		if err != nil {
			return nil, err
		}
		if len(sections) == 0 {
			return nil, nil
		}
		return requiredSections{typ: "meeting", sections: sections}, nil
	})
	lint.Register("person-meta-last-updated", func(cfg map[string]any) (any, error) {
		return metaLastUpdated{}, nil
	})
	lint.Register("person-meeting-history-bullet-format", func(cfg map[string]any) (any, error) {
		return historyBulletFormat{}, nil
	})
	lint.Register("daily-brief-per-day", func(cfg map[string]any) (any, error) {
		return dailyBriefPerDay{}, nil
	})
}

// ---- required sections (person / meeting) -----------------------------------

type requiredSections struct {
	typ      string
	sections []string
}

func (r requiredSections) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if typedNote(ctx, n) != r.typ || n.TooLarge {
		return nil
	}
	var out []lint.Finding
	for _, s := range r.sections {
		found := false
		for _, h := range h2s(n) {
			if strings.HasPrefix(h, s) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, finding(lint.Warning, n.RelPath, 0, true,
				"missing required section '## %s'", s))
		}
	}
	return out
}

var missingSectionRe = regexp.MustCompile(`^missing required section '## (.+)'$`)

// Fix appends the missing heading in template position: after the previous
// template section when present, else at end of file. Content-free and
// idempotent (docs/LINT-RULES.md).
func (r requiredSections) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	m := missingSectionRe.FindStringSubmatch(f.Message)
	if m == nil {
		return nil, nil
	}
	section := m[1]
	rank := -1
	for i, s := range r.sections {
		if s == section {
			rank = i
		}
	}
	if rank < 0 {
		return nil, nil
	}
	// Find the line of the latest present section that precedes this one
	// in the template; the new heading goes before the NEXT section after
	// it, or at EOF.
	insertAfter := len(srcLines(n.Src)) // default: EOF
	for lineNo, h := range h2s(n) {
		for i := rank + 1; i < len(r.sections); i++ {
			if strings.HasPrefix(h, r.sections[i]) && lineNo-1 < insertAfter {
				insertAfter = lineNo - 1
			}
		}
	}
	block := []byte("\n## " + section + "\n")
	if insertAfter == len(srcLines(n.Src)) {
		newSrc := append(append([]byte(nil), n.Src...), block...)
		return &lint.FixResult{NewSrc: newSrc}, nil
	}
	newSrc, ok := insertLineAfter(n.Src, insertAfter, []byte("## "+section+"\n"))
	if !ok {
		return nil, nil
	}
	return &lint.FixResult{NewSrc: newSrc}, nil
}

// ---- person-meta-last-updated ------------------------------------------------

var lastUpdatedRe = regexp.MustCompile(`^- \*\*Last Updated\*\*: \d{4}-\d{2}-\d{2}$`)

type metaLastUpdated struct{}

func (metaLastUpdated) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if typedNote(ctx, n) != "person" {
		return nil
	}
	lines := sectionLines(n, "Meta")
	if len(lines) == 0 {
		return nil // missing section is person-required-sections' finding
	}
	for _, text := range lines {
		if lastUpdatedRe.MatchString(text) {
			return nil
		}
	}
	for _, text := range lines {
		if strings.HasPrefix(text, "- **Last Updated**:") {
			return []lint.Finding{finding(lint.Warning, n.RelPath, 0, false,
				"Meta 'Last Updated' bullet is not '- **Last Updated**: YYYY-MM-DD'")}
		}
	}
	return []lint.Finding{finding(lint.Warning, n.RelPath, 0, false,
		"Meta section has no '- **Last Updated**: YYYY-MM-DD' bullet")}
}

// ---- person-meeting-history-bullet-format ---------------------------------------

var historyBulletShapeRe = regexp.MustCompile(`^- \*\*\d{4}-\d{2}-\d{2}\*\* — \[\[.+\]\] — .+$`)

type historyBulletFormat struct{}

func (historyBulletFormat) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if typedNote(ctx, n) != "person" {
		return nil
	}
	var out []lint.Finding
	for lineNo, text := range sectionLines(n, "Meeting History") {
		if !strings.HasPrefix(text, "- ") {
			continue // only top-level bullets are checked
		}
		if !historyBulletShapeRe.MatchString(text) {
			out = append(out, finding(lint.Warning, n.RelPath, lineNo, false,
				"history bullet must be '- **YYYY-MM-DD** — [[link]] — summary'"))
		}
	}
	return out
}

// ---- daily-brief-per-day -----------------------------------------------------------

type dailyBriefPerDay struct{}

func (dailyBriefPerDay) CheckVault(ctx *lint.Context) []lint.Finding {
	days := map[string]bool{}   // day dirs containing >=1 meeting note
	briefs := map[string]bool{} // day dirs containing daily-brief.md
	for _, f := range sortedFiles(ctx) {
		dir := pathDir(f)
		if !meetingDayDirRe.MatchString(dir) {
			continue
		}
		if meetingNameRe.MatchString(baseOf(f)) {
			days[dir] = true
		}
		if baseOf(f) == "daily-brief.md" {
			briefs[dir] = true
		}
	}
	var out []lint.Finding
	var sorted []string
	for d := range days {
		if !briefs[d] {
			sorted = append(sorted, d)
		}
	}
	sort.Strings(sorted)
	for _, d := range sorted {
		out = append(out, finding(lint.Warning, d, 0, false,
			"meeting day has no daily-brief.md"))
	}
	return out
}

func pathDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}
