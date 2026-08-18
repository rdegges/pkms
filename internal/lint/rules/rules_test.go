package rules_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
	_ "github.com/rdegges/pkms/internal/lint/rules"
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

// buildVault writes a synthetic vault and lints it with the rdegges profile.
func buildVault(t *testing.T, files map[string]string) (*vault.Index, *profile.Profile, string) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	ix, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	require.NoError(t, err)
	return ix, prof, root
}

func run(t *testing.T, files map[string]string, only ...string) []lint.Finding {
	t.Helper()
	ix, prof, _ := buildVault(t, files)
	fs, err := lint.Run(ix, prof, nil, only)
	require.NoError(t, err)
	return fs
}

func byRule(fs []lint.Finding) map[string][]lint.Finding {
	out := map[string][]lint.Finding{}
	for _, f := range fs {
		out[f.Rule] = append(out[f.Rule], f)
	}
	return out
}

// validMeeting is a fully conforming meeting note.
const validMeeting = `---
date: 2026-05-06
time: "11:00 - 12:00"
duration: 60
type: meeting
has_transcript: true
attendees:
  - "[[Jane Doe]]"
tags:
  - meeting
  - snyk
---
## Attendees
- [[Jane Doe]]

## Summary
Things happened.

## Key Decisions
- None.
`

const validPerson = `---
last_met: 2026-05-06
meeting_count: 1
topics:
  - ai-security
---
## Meta
- **Last Updated**: 2026-05-06

## Relationship
Colleague.

## Meeting History
- **2026-05-06** — [[1100 - Weekly Sync]] — Discussed things.
`

func cleanVault() map[string]string {
	return map[string]string{
		"People/Snyk/Jane Doe.md":                        validPerson,
		"Meetings/Snyk/2026/05/06/1100 - Weekly Sync.md": validMeeting,
		"Meetings/Snyk/2026/05/06/daily-brief.md": `---
date: 2026-05-06
type: daily-brief
tags: [meetings, snyk, daily-brief]
---
Brief.
`,
		"Now.md": `# Now

## Today
- Ship pkms.

## Active Projects
- One
- Two
- Three
- Four
- Five

## Notes
- [[Jane Doe]] is helpful.
`,
		"log.md": `# Log

## [2026-05-07] update | Newer entry
- did stuff

## [2026-05-06] create | Older entry
- made a thing
`,
	}
}

func TestCleanVaultHasNoFindings(t *testing.T) {
	fs := run(t, cleanVault())
	require.Empty(t, fs, "%+v", fs)
}

func TestFrontmatterRules(t *testing.T) {
	fs := run(t, map[string]string{
		"People/Snyk/No FM.md":     "# Just a body\n",
		"People/Snyk/Unclosed.md":  "---\nlast_met: 2026-01-01\n# body\n",
		"People/Snyk/Tabs.md":      "---\nlast_met: 2026-01-01\nmeeting_count: 1\ntopics:\n\t- ai\n---\nx\n",
		"People/Snyk/Bad Date.md":  "---\nlast_met: 2026/01/02\nmeeting_count: 1\ntopics: [ai]\n---\nx\n",
		"People/Snyk/Null Date.md": "---\nlast_met: null\nmeeting_count: 1\ntopics: [ai]\n---\nx\n",
		"People/Snyk/Ambiguous.md": "---\nlast_met: 05/07/2026\nmeeting_count: 1\ntopics: [ai]\n---\nx\n",
	}, "frontmatter-present", "frontmatter-closed", "frontmatter-no-tabs", "date-format-iso")
	m := byRule(fs)

	require.Len(t, m["frontmatter-present"], 1)
	require.Equal(t, lint.Error, m["frontmatter-present"][0].Severity)
	require.False(t, m["frontmatter-present"][0].Fixable, "person FM values not derivable")

	require.Len(t, m["frontmatter-closed"], 1)
	require.True(t, m["frontmatter-closed"][0].Fixable)

	require.Len(t, m["frontmatter-no-tabs"], 1)
	require.True(t, m["frontmatter-no-tabs"][0].Fixable)

	dates := m["date-format-iso"]
	require.Len(t, dates, 3)
	fixables := 0
	for _, f := range dates {
		if f.Fixable {
			fixables++
		}
	}
	require.Equal(t, 1, fixables, "only 2026/01/02 is unambiguous: %+v", dates)
}

func TestDateRuleAcceptsRFC3339Timestamps(t *testing.T) {
	// The ingest pipeline stamps `created` as RFC3339 with offset (SPEC
	// §20); the rule must not flag every ingested note (SPEC §31.10 —
	// latent phase-2 conflict caught by the asset e2e).
	fs := run(t, map[string]string{
		"People/Snyk/Stamped.md": "---\nlast_met: 2026-08-09T12:34:56Z\nmeeting_count: 1\ntopics: [ai]\n---\nx\n",
		"People/Snyk/Offset.md":  "---\nlast_met: 2026-08-09T12:34:56-07:00\nmeeting_count: 1\ntopics: [ai]\n---\nx\n",
		"People/Snyk/Trunc.md":   "---\nlast_met: 2026-08-09T12:34\nmeeting_count: 1\ntopics: [ai]\n---\nx\n",
	}, "date-format-iso")
	m := byRule(fs)
	dates := m["date-format-iso"]
	require.Len(t, dates, 1, "full RFC3339 passes; a truncated timestamp still fails: %+v", dates)
	require.Contains(t, dates[0].Path, "Trunc")
}

func TestSchemaRule(t *testing.T) {
	fs := run(t, map[string]string{
		// Missing has_transcript + duration; wrong time type.
		"Meetings/Snyk/2026/05/06/1100 - Broken.md": `---
date: 2026-05-06
time: "11:00 - 12:00"
type: meeting
attendees: []
tags: [meeting, snyk]
---
x
`,
		// Project missing everything → warnings (warning_types).
		"Projects/Personal/loose.md": "---\nstatus: active\n---\nx\n",
		// Person with a wrong-typed field: person is in warning_types
		// (pre-schema history at scale, like meeting).
		"People/Snyk/Broken Person.md": "---\nlast_met: 2026-05-06\nmeeting_count: lots\ntopics: [ai]\n---\nx\n",
		// The shape that actually put person on the list: a pre-schema note
		// with none of the required keys, not merely a mistyped one.
		"People/Snyk/Pre-Schema Person.md": "---\nname: Jane\n---\nx\n",
		// Daily-brief missing tags: NOT in warning_types, so this must
		// stay an error — pins the default severity path.
		"Meetings/Snyk/2026/05/06/daily-brief.md": "---\ndate: 2026-05-06\ntype: daily-brief\n---\nx\n",
	}, "frontmatter-schema")
	m := byRule(fs)
	require.NotEmpty(t, m["frontmatter-schema"])

	// Every fixture must actually produce a finding. Without this, a fixture
	// that stops failing the schema silently skips its severity assertion
	// below and the test still passes.
	// project/meeting/person are in warning_types because the reference
	// vault's history predates their schemas; daily-brief is not, so it
	// keeps the default Error severity.
	wantSeverity := map[string]lint.Severity{
		"Projects/Personal/loose.md":                lint.Warning,
		"Meetings/Snyk/2026/05/06/1100 - Broken.md": lint.Warning,
		"People/Snyk/Broken Person.md":              lint.Warning,
		"People/Snyk/Pre-Schema Person.md":          lint.Warning,
		"Meetings/Snyk/2026/05/06/daily-brief.md":   lint.Error,
	}
	seen := map[string]bool{}
	for _, f := range m["frontmatter-schema"] {
		seen[f.Path] = true
		want, ok := wantSeverity[f.Path]
		if !ok {
			want = lint.Error // default severity: type not in warning_types
		}
		require.Equal(t, want, f.Severity, "%s: %s", f.Path, f.Message)
	}
	for path := range wantSeverity {
		require.True(t, seen[path], "fixture %s produced no schema finding", path)
	}
}

func TestSessionTraceFrontmatterFix(t *testing.T) {
	files := map[string]string{
		"Resources/Personal/Session Traces/2026-07-10 — my-trace.md": "# Session trace\nno frontmatter\n",
	}
	ix, prof, root := buildVault(t, files)
	fs, err := lint.Run(ix, prof, nil, []string{"frontmatter-present"})
	require.NoError(t, err)
	require.Len(t, fs, 1)
	require.Equal(t, lint.Warning, fs[0].Severity, "session-trace is a warning type")
	require.True(t, fs[0].Fixable)

	res, err := lint.Fix(ix, prof, nil, fs[0])
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Contains(t, string(res.NewSrc), "date: 2026-07-10")
	require.Contains(t, string(res.NewSrc), `slug: "my-trace"`, "slug is quoted (YAML-special chars)")

	// Idempotence: after applying, the finding is gone and Fix is a no-op.
	p := filepath.Join(root, "Resources/Personal/Session Traces/2026-07-10 — my-trace.md")
	require.NoError(t, os.WriteFile(p, res.NewSrc, 0o644))
	ix2, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	require.NoError(t, err)
	fs2, err := lint.Run(ix2, prof, nil, []string{"frontmatter-present"})
	require.NoError(t, err)
	require.Empty(t, fs2)
}

func TestPathRules(t *testing.T) {
	fs := run(t, map[string]string{
		"Meetings/Snyk/2026/05/06/1100 - Mismatch.md": `---
date: 2026-05-07
time: "13:00 - 14:00"
duration: 60
type: meeting
has_transcript: false
attendees: []
tags: [meeting]
---
x
`,
		"Projects/Snyk/Wrong Cat.md": `---
type: project
category: Personal
status: active
created: 2026-01-01
updated: 2026-01-01
description: x
---
x
`,
	}, "meeting-date-matches-path", "meeting-time-matches-filename", "meeting-tags-domain", "project-category-matches-path")
	m := byRule(fs)
	require.Len(t, m["meeting-date-matches-path"], 1)
	require.Len(t, m["meeting-time-matches-filename"], 1)
	require.Len(t, m["meeting-tags-domain"], 1)
	require.True(t, m["meeting-tags-domain"][0].Fixable)
	require.Len(t, m["project-category-matches-path"], 1)
	require.True(t, m["project-category-matches-path"][0].Fixable)
}

func TestNamingAndPlacementRules(t *testing.T) {
	fs := run(t, map[string]string{
		"Rogue Root.md":                                "x\n",
		"now.md":                                       "x\n",
		"Extra/whatever.md":                            "x\n",
		"Projects/loose-note.md":                       "x\n",
		"Projects/Wrong/n.md":                          "x\n",
		"Meetings/Snyk/2026/5/6/1100 - Bad.md":         "x\n",
		"Meetings/Snyk/2026/05/07/Untitled Meeting.md": "x\n",
		"Resources/Clips/Inbox/random-clip.md":         "x\n",
		"Resources/Personal/Session Traces/2026-07-10 - hyphen.md":     "x\n",
		"Projects/Snyk/Slack Sync 2026-05-17.md":                       "x\n",
		"People/Snyk/backup.md.bak":                                    "x\n",
		"Action Items.md.bak":                                          "x\n",
		"Projects.base":                                                "x\n",
		"Projects/Snyk/Roadmap (Conflicted copy iPhone 2026-08-17).md": "x\n",
	}, "root-canonical-only", "root-file-name-case", "top-level-folders-fixed",
		"domain-split-folders", "meeting-path-valid-date", "meeting-filename-format",
		"clip-processed-filename", "session-trace-filename", "no-per-run-notes", "no-junk-files")
	m := byRule(fs)

	rootPaths := []string{}
	for _, f := range m["root-canonical-only"] {
		rootPaths = append(rootPaths, f.Path)
	}
	require.Contains(t, rootPaths, "Rogue Root.md")
	require.Contains(t, rootPaths, "Action Items.md.bak")
	require.NotContains(t, rootPaths, "now.md", "case variants belong to root-file-name-case")
	require.NotContains(t, rootPaths, "Projects.base", "Obsidian Bases file is allowlisted at root")

	require.Len(t, m["root-file-name-case"], 1)
	require.Equal(t, "now.md", m["root-file-name-case"][0].Path)

	require.Len(t, m["top-level-folders-fixed"], 1)
	require.Equal(t, "Extra", m["top-level-folders-fixed"][0].Path)

	splitPaths := []string{}
	for _, f := range m["domain-split-folders"] {
		splitPaths = append(splitPaths, f.Path)
	}
	require.Contains(t, splitPaths, "Projects/loose-note.md")
	require.Contains(t, splitPaths, "Projects/Wrong")

	require.NotEmpty(t, m["meeting-path-valid-date"])
	require.Len(t, m["meeting-filename-format"], 1)
	require.Len(t, m["clip-processed-filename"], 1)
	require.Len(t, m["session-trace-filename"], 1)
	require.True(t, m["session-trace-filename"][0].Fixable, "hyphen form with no inbound links")
	require.Len(t, m["no-per-run-notes"], 1)
	junkPaths := []string{}
	for _, f := range m["no-junk-files"] {
		junkPaths = append(junkPaths, f.Path)
	}
	require.Contains(t, junkPaths, "Projects/Snyk/Roadmap (Conflicted copy iPhone 2026-08-17).md",
		"Obsidian Sync capitalizes Conflicted; the pattern must match its real output")
	require.Contains(t, junkPaths, "People/Snyk/backup.md.bak")
	require.NotContains(t, junkPaths, "Projects.base", "an allowlisted root file is not junk")
}

func TestLinkRules(t *testing.T) {
	files := cleanVault()
	files["Resources/Personal/Links.md"] = `---
source_url: https://example.com/x
date_clipped: 2026-05-06
topics: [stuff]
related_people:
  - "[[Jane Doe]]"
  - "[[Nobody Known]]"
related_projects: []
tags: [clip]
---
A broken [[Jane Doee]] link (typo, fixable).
A working [[Jane Doe]] link.
A broken anchor [[Jane Doe#Nonexistent Heading]].
`
	fs := run(t, files, "wikilink-resolves", "related-people-resolve-to-people")
	m := byRule(fs)

	var typo, anchor int
	for _, f := range m["wikilink-resolves"] {
		switch {
		case f.Severity == lint.Error && strings.Contains(f.Message, "Jane Doee"):
			typo++
			require.True(t, f.Fixable, "Jane Doee → Jane Doe is a unique Levenshtein-1 repair: %+v", f)
		case f.Severity == lint.Warning:
			anchor++
		}
	}
	require.Equal(t, 1, typo)
	require.Equal(t, 1, anchor, "broken heading anchor is a warning")

	require.Len(t, m["related-people-resolve-to-people"], 1)
	require.Contains(t, m["related-people-resolve-to-people"][0].Message, "Nobody Known")
}

func TestAmbiguousAndDuplicate(t *testing.T) {
	files := cleanVault()
	files["Resources/Snyk/Note.md"] = "x\n"
	files["Resources/Personal/Note.md"] = "x\n"
	files["Areas/Personal/links.md"] = "See [[Note]].\n"
	fs := run(t, files, "wikilink-ambiguous", "duplicate-basename")
	m := byRule(fs)
	require.Len(t, m["wikilink-ambiguous"], 1)
	require.Len(t, m["duplicate-basename"], 2)
}

func TestCountDriftAndFix(t *testing.T) {
	files := map[string]string{
		"Action Items.md": `---
updated: 2026-05-06
total_items: 5
---
- [ ] one
- [x] two
  - [ ] nested three
`,
		"Resources/Personal/Recipes/Recipes.md": `---
type: resource
sub_domain: recipes
recipe_count: 14
last_updated: 2026-05-06
---
## By course
- [[Beef Stew]]
`,
		"Resources/Personal/Recipes/Beef Stew.md": `---
type: recipe
servings: 4
course: main
tags: [recipe]
---
Stew.
`,
	}
	ix, prof, _ := buildVault(t, files)
	fs, err := lint.Run(ix, prof, nil, []string{"action-items-count-drift", "recipes-count-drift"})
	require.NoError(t, err)
	m := byRule(fs)

	require.Len(t, m["action-items-count-drift"], 1)
	require.Contains(t, m["action-items-count-drift"][0].Message, "5 declared but 3 counted")
	require.True(t, m["action-items-count-drift"][0].Fixable)

	require.Len(t, m["recipes-count-drift"], 1)
	require.Contains(t, m["recipes-count-drift"][0].Message, "14 declared but 1 counted")

	// Fix recomputes; result is minimal-diff (only the count line changes).
	res, err := lint.Fix(ix, prof, nil, m["action-items-count-drift"][0])
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Contains(t, string(res.NewSrc), "total_items: 3")
	require.Contains(t, string(res.NewSrc), "updated: 2026-05-06", "unrelated lines untouched")
}

func TestIndexCompleteness(t *testing.T) {
	files := cleanVault()
	files["Projects.md"] = "# Projects\n- [[Cataloged]]\n"
	files["Projects/Snyk/Cataloged.md"] = "---\ntype: project\ncategory: Snyk\nstatus: active\ncreated: 2026-01-01\nupdated: 2026-01-01\ndescription: x\n---\nx\n"
	files["Projects/Snyk/Uncataloged.md"] = "---\ntype: project\ncategory: Snyk\nstatus: active\ncreated: 2026-01-01\nupdated: 2026-01-01\ndescription: x\n---\nx\n"
	files["index.md"] = "# Index\nNav only.\n"
	fs := run(t, files, "projects-linked-from-master")
	m := byRule(fs)
	require.Len(t, m["projects-linked-from-master"], 1)
	require.Equal(t, "Projects/Snyk/Uncataloged.md", m["projects-linked-from-master"][0].Path)
	require.Equal(t, lint.Warning, m["projects-linked-from-master"][0].Severity)
}

func TestLogRules(t *testing.T) {
	files := cleanVault()
	files["log.md"] = `# Log

## [2026-05-06] update | Fine entry
- ok

## 2026-05-07 broken header
- x

## [2026-05-08] weird-action | Vocab miss
- y

## [2026-05-09] update | Out of order (newer below older)
- z

## [2026-05-01] update | No bullets here
just prose
`
	fs := run(t, files, "log-entry-format", "log-action-vocab", "log-newest-first")
	m := byRule(fs)
	require.Len(t, m["log-entry-format"], 2, "bad header + missing bullets: %+v", m["log-entry-format"])
	require.Len(t, m["log-action-vocab"], 1)
	require.Len(t, m["log-newest-first"], 2, "2026-05-08 and 2026-05-09 both appear below newer dates")
}

func TestNowRules(t *testing.T) {
	files := cleanVault()
	long := "# Now\n\n## Today\n- x\n\n## Active Projects\n- a\n- b\n  - nested\n\n## Watching\n- w\n\n## Blocked / Waiting On\n- b\n\n### New from 2026-05-18 Slack sync\n- bad\n"
	for i := 0; i < 80; i++ {
		long += "- filler line\n"
	}
	files["Now.md"] = long
	fs := run(t, files, "now-line-cap", "now-fixed-sections", "now-no-sync-sections", "now-active-projects-shape")
	m := byRule(fs)

	require.Len(t, m["now-line-cap"], 1)
	require.Equal(t, lint.Error, m["now-line-cap"][0].Severity, "80+ lines is past the hard cap")
	require.Len(t, m["now-fixed-sections"], 1, "Watching before Blocked is out of order: %+v", m["now-fixed-sections"])
	require.Len(t, m["now-no-sync-sections"], 1)
	require.NotEmpty(t, m["now-active-projects-shape"])
}

func TestStructureRules(t *testing.T) {
	files := cleanVault()
	files["People/Snyk/Sparse.md"] = `---
last_met: 2026-05-06
meeting_count: 0
topics: []
---
## Meta
- **Last Updated**: May 2026

## Relationship
Friend.
`
	fs := run(t, files, "person-required-sections", "person-meta-last-updated", "person-meeting-history-bullet-format")
	m := byRule(fs)
	require.Len(t, m["person-required-sections"], 1)
	require.Contains(t, m["person-required-sections"][0].Message, "Meeting History")
	require.True(t, m["person-required-sections"][0].Fixable)
	require.Len(t, m["person-meta-last-updated"], 1)
}

func TestDailyBriefPerDay(t *testing.T) {
	files := cleanVault()
	files["Meetings/Snyk/2026/05/09/1400 - Lonely Meeting.md"] = validMeetingOn("2026-05-09", "14:00 - 15:00")
	fs := run(t, files, "daily-brief-per-day")
	require.Len(t, fs, 1)
	require.Equal(t, "Meetings/Snyk/2026/05/09", fs[0].Path)
}

func validMeetingOn(date, timeRange string) string {
	return `---
date: ` + date + `
time: "` + timeRange + `"
duration: 60
type: meeting
has_transcript: false
attendees: []
tags: [meeting, snyk]
---
## Attendees

## Summary
s

## Key Decisions
- none
`
}

// TestUnclosedFenceFixNeverSwallowsBody: the closing fence goes before the
// first non-YAML-shaped line (blank line / heading / prose), never after it
// — the longest-parseable-prefix approach ate body content (markdown
// headings are valid YAML comments). Regression from red-team review.
func TestUnclosedFenceFixNeverSwallowsBody(t *testing.T) {
	files := map[string]string{
		"People/Snyk/Unclosed.md": "---\nlast_met: 2026-01-01\nmeeting_count: 2\n# Jane Doe\n\nSome relationship text.\n",
	}
	ix, prof, _ := buildVault(t, files)
	fs, err := lint.Run(ix, prof, nil, []string{"frontmatter-closed"})
	require.NoError(t, err)
	require.Len(t, fs, 1)

	res, err := lint.Fix(ix, prof, nil, fs[0])
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t,
		"---\nlast_met: 2026-01-01\nmeeting_count: 2\n---\n# Jane Doe\n\nSome relationship text.\n",
		string(res.NewSrc), "the H1 and prose stay in the body")

	// All-prose after the fence: nothing unambiguous to close — report-only.
	files2 := map[string]string{
		"People/Snyk/AllProse.md": "---\nJust prose, no keys at all\nmore prose\n",
	}
	ix2, prof2, _ := buildVault(t, files2)
	fs2, err := lint.Run(ix2, prof2, nil, []string{"frontmatter-closed"})
	require.NoError(t, err)
	require.Len(t, fs2, 1)
	res2, err := lint.Fix(ix2, prof2, nil, fs2[0])
	require.NoError(t, err)
	require.Nil(t, res2, "no unambiguous repair exists")
}

// TestWikilinkFixNeverRewritesAdjacentValidLink: repairing [[Foo]] must not
// touch a valid [[Foobar]] earlier on the same line (codex finding: the
// old prefix match `[[Foo` hit both).
func TestWikilinkFixNeverRewritesAdjacentValidLink(t *testing.T) {
	files := map[string]string{
		"Foobar.md": "# Foobar\n",
		"Fooo.md":   "# Fooo\n",
		"Source.md": "See [[Foobar]] and broken [[Foo]] here.\n",
	}
	ix, prof, _ := buildVault(t, files)
	fs, err := lint.Run(ix, prof, nil, []string{"wikilink-resolves"})
	require.NoError(t, err)
	require.Len(t, fs, 1)
	require.Contains(t, fs[0].Message, "[[Foo]]")
	require.True(t, fs[0].Fixable, "unique distance-1 candidate Fooo")

	res, err := lint.Fix(ix, prof, nil, fs[0])
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Contains(t, string(res.NewSrc), "[[Foobar]]", "valid link untouched")
	require.Contains(t, string(res.NewSrc), "[[Fooo]]", "broken link repaired")
}

// TestAttendeeHashNeverWrapped: "Jane#CEO" must not become a
// fragment-bearing [[Jane#CEO]] link (codex finding), and fragment/embed
// attendee entries are rejected by the checker.
func TestAttendeeHashNeverWrapped(t *testing.T) {
	files := cleanVault()
	files["Meetings/Snyk/2026/05/07/1000 - Odd.md"] = `---
date: 2026-05-07
time: "10:00 - 11:00"
duration: 60
type: meeting
has_transcript: false
attendees:
  - Jane#CEO
  - "[[Jane Doe#CEO]]"
  - "![[Jane Doe]]"
tags: [meeting, snyk]
---
x
`
	ix, prof, _ := buildVault(t, files)
	fs, err := lint.Run(ix, prof, nil, []string{"meeting-attendees-are-wikilinks"})
	require.NoError(t, err)
	require.Len(t, fs, 3)
	for _, f := range fs {
		require.False(t, f.Fixable, "%s", f.Message)
		res, ferr := lint.Fix(ix, prof, nil, f)
		require.NoError(t, ferr)
		require.Nil(t, res)
	}
}

// TestBracketedTargetNeverFixable: a real filename containing wikilink
// syntax chars ([C]) must never produce a single-repair fix — the repair
// cannot round-trip through [[...]] parsing and corrupts the link
// (regression: bracket growth on every --fix run).
func TestBracketedTargetNeverFixable(t *testing.T) {
	files := map[string]string{
		"People/Snyk/Janet Giesen [C].md": validPerson,
		"log.md":                          "# Log\n\n## [2026-05-06] update | x\n- remediated [[Janet Giesen [C]]] today\n",
	}
	ix, prof, _ := buildVault(t, files)
	fs, err := lint.Run(ix, prof, nil, []string{"wikilink-resolves"})
	require.NoError(t, err)
	for _, f := range fs {
		require.False(t, f.Fixable, "bracketed targets are never auto-repaired: %+v", f)
		res, ferr := lint.Fix(ix, prof, nil, f)
		require.NoError(t, ferr)
		require.Nil(t, res)
	}
}

// TestKeyOrderRule: off by default; when enabled with explicit orders,
// checks template order and fixes by moving whole line spans.
func TestKeyOrderRule(t *testing.T) {
	files := map[string]string{
		"People/Snyk/Shuffled.md": "---\ntopics:\n  - ai\nlast_met: 2026-01-01\nmeeting_count: 1\n---\nbody\n",
	}
	ix, prof, _ := buildVault(t, files)

	// Disabled (profile default) → no findings.
	fs, err := lint.Run(ix, prof, nil, []string{"frontmatter-key-order"})
	require.NoError(t, err)
	require.Empty(t, fs)

	overrides := map[string]map[string]any{
		"frontmatter-key-order": {
			"enabled": true,
			"orders":  map[string]any{"person": []any{"last_met", "meeting_count", "topics"}},
		},
	}
	fs, err = lint.Run(ix, prof, overrides, []string{"frontmatter-key-order"})
	require.NoError(t, err)
	require.Len(t, fs, 1)
	require.True(t, fs[0].Fixable)

	res, err := lint.Fix(ix, prof, overrides, fs[0])
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t,
		"---\nlast_met: 2026-01-01\nmeeting_count: 1\ntopics:\n  - ai\n---\nbody\n",
		string(res.NewSrc), "line spans move verbatim, values untouched")
}

// TestFixIdempotence: applying every fix converges and a second pass
// changes nothing (fix twice = no-op, SPEC §8).
func TestFixIdempotence(t *testing.T) {
	files := map[string]string{
		"People/Snyk/Fixme.md": `---
last_met: 2026/01/02
meeting_count: 1
topics:
  - AI Security
---
## Meta
- **Last Updated**: 2026-01-02

## Relationship
r
`,
	}
	ix, prof, root := buildVault(t, files)

	applyAll := func() int {
		applied := 0
		for iter := 0; iter < 10; iter++ {
			ix2, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: prof.Attachments})
			require.NoError(t, err)
			fs, err := lint.Run(ix2, prof, nil, nil)
			require.NoError(t, err)
			progressed := false
			for _, f := range fs {
				if !f.Fixable {
					continue
				}
				res, err := lint.Fix(ix2, prof, nil, f)
				require.NoError(t, err)
				if res == nil || res.RenameTo != "" {
					continue
				}
				p := filepath.Join(root, filepath.FromSlash(f.Path))
				require.NoError(t, os.WriteFile(p, res.NewSrc, 0o644))
				applied++
				progressed = true
				break // one fix per iteration, then re-lint
			}
			if !progressed {
				break
			}
		}
		return applied
	}

	first := applyAll()
	require.Greater(t, first, 0, "fixture must exercise at least one fix")
	second := applyAll()
	require.Zero(t, second, "second pass must be a no-op")

	got, err := os.ReadFile(filepath.Join(root, "People/Snyk/Fixme.md"))
	require.NoError(t, err)
	require.Contains(t, string(got), "last_met: 2026-01-02")
	require.Contains(t, string(got), "- ai-security")
	_ = ix
}
