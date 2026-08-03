package rules_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rdegges/pkms/internal/lint"
	_ "github.com/rdegges/pkms/internal/lint/rules"
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
	"github.com/stretchr/testify/require"
)

func buildLintVault(t *testing.T, files map[string]string) (*vault.Index, *profile.Profile) {
	t.Helper()
	root := t.TempDir()
	for rel, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	}

	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	require.NotNil(t, prof)

	ix, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	require.NoError(t, err)
	require.NotNil(t, ix)
	return ix, prof
}

func runOnly(t *testing.T, files map[string]string, rule string) []lint.Finding {
	t.Helper()
	ix, prof := buildLintVault(t, files)
	findings, err := lint.Run(ix, prof, nil, []string{rule})
	require.NoError(t, err)
	for _, finding := range findings {
		require.Equal(t, rule, finding.Rule)
	}
	return findings
}

func requireFinding(t *testing.T, finding lint.Finding, rule string, severity lint.Severity, path string) {
	t.Helper()
	require.Equal(t, rule, finding.Rule)
	require.Equal(t, severity, finding.Severity)
	require.Equal(t, path, finding.Path)
}

func TestDateFormatISOChecksEveryScopedFrontmatterKey(t *testing.T) {
	t.Parallel()

	keys := []string{
		"date",
		"last_met",
		"created",
		"updated",
		"last_updated",
		"date_clipped",
		"date_published",
		"published",
	}
	files := make(map[string]string, len(keys))
	wantPaths := make([]string, len(keys))
	for i, key := range keys {
		path := key + ".md"
		files[path] = fmt.Sprintf("---\n%s: not-a-date\n---\nbody\n", key)
		wantPaths[i] = path
	}
	sort.Strings(wantPaths)

	findings := runOnly(t, files, "date-format-iso")
	require.Len(t, findings, len(keys))
	gotPaths := make([]string, len(findings))
	for i, finding := range findings {
		require.Equal(t, lint.Error, finding.Severity)
		require.False(t, finding.Fixable)
		gotPaths[i] = finding.Path
	}
	require.Equal(t, wantPaths, gotPaths)
}

func TestDateFormatISOAcceptsStrictRealCalendarDates(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"AllKeys.md": strings.Join([]string{
			"---",
			"date: 2026-07-15",
			"last_met: 2026-07-15",
			"created: 2026-07-15",
			"updated: 2026-07-15",
			"last_updated: 2026-07-15",
			"date_clipped: 2026-07-15",
			"date_published: 2026-07-15",
			"published: 2024-02-29",
			"---",
			"body",
		}, "\n"),
	}
	require.Empty(t, runOnly(t, files, "date-format-iso"))
}

func TestDateFormatISORejectsBadShapesAndImpossibleDatesWithSpecifiedFixability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		yaml             string
		fixable          bool
		assertFixability bool
	}{
		{name: "slash format", yaml: "date: 2026/07/15", fixable: true, assertFixability: true},
		{name: "long month format", yaml: "date: July 15, 2026", fixable: true, assertFixability: true},
		{name: "ambiguous numeric format", yaml: "date: 05/07/2026", fixable: false, assertFixability: true},
		{name: "empty", yaml: "date:", fixable: false, assertFixability: true},
		{name: "explicit null", yaml: "date: null", fixable: false, assertFixability: true},
		{name: "impossible month", yaml: "date: 2026-13-01", fixable: false, assertFixability: true},
		{name: "impossible day", yaml: "date: 2026-02-29", fixable: false, assertFixability: true},
		{name: "not strict two digit shape", yaml: "date: 2026-7-5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := runOnly(t, map[string]string{
				"Date.md": "---\n" + tt.yaml + "\n---\nbody\n",
			}, "date-format-iso")
			require.Len(t, findings, 1)
			requireFinding(t, findings[0], "date-format-iso", lint.Error, "Date.md")
			if tt.assertFixability {
				require.Equal(t, tt.fixable, findings[0].Fixable)
			}
		})
	}
}

func TestFrontmatterClosedReportsUnclosedFenceAtLineOne(t *testing.T) {
	t.Parallel()

	findings := runOnly(t, map[string]string{
		"Unclosed.md": "---\ntitle:\tNever closed\nbody is frontmatter now\n",
		"Closed.md":   "---\ntitle: Closed\n---\nbody\n",
		"NoFM.md":     "ordinary body\n",
	}, "frontmatter-closed")
	require.Len(t, findings, 1)
	requireFinding(t, findings[0], "frontmatter-closed", lint.Error, "Unclosed.md")
	require.Equal(t, 1, findings[0].Line)
	require.True(t, findings[0].Fixable)
}

func TestFrontmatterNoTabsChecksOnlyFrontmatterAndFixability(t *testing.T) {
	t.Parallel()

	findings := runOnly(t, map[string]string{
		"LeadingOnly.md": "---\nparent:\n\tchild: value\n---\nbody\n",
		"Internal.md":    "---\ntitle:\tvalue\n---\nbody\n",
		"BodyOnly.md":    "---\ntitle: clean\n---\nbody\twith tab\n",
		"NoTabs.md":      "---\ntitle: clean\n---\nbody\n",
	}, "frontmatter-no-tabs")
	require.Len(t, findings, 2)
	requireFinding(t, findings[0], "frontmatter-no-tabs", lint.Error, "Internal.md")
	require.False(t, findings[0].Fixable)
	requireFinding(t, findings[1], "frontmatter-no-tabs", lint.Error, "LeadingOnly.md")
	require.True(t, findings[1].Fixable)
}

func TestMeetingDateMatchesPathChecksOnlyMeetingDayDirectories(t *testing.T) {
	t.Parallel()

	findings := runOnly(t, map[string]string{
		"Meetings/Snyk/2026/07/15/0900 - Matches.md": "---\ndate: 2026-07-15\n---\nbody\n",
		"Meetings/Snyk/2026/07/15/1000 - Differs.md": "---\ndate: 2026-07-14\n---\nbody\n",
		"Meetings/Snyk/2026/07/15/1100 - Missing.md": "---\ntitle: Missing date\n---\nbody\n",
		"Meetings/Snyk/Elsewhere.md":                 "---\ndate: 2026-07-14\n---\nbody\n",
		"Other/2026/07/15/0900 - Outside.md":         "---\ndate: 2026-07-14\n---\nbody\n",
	}, "meeting-date-matches-path")
	require.Len(t, findings, 1)
	requireFinding(t, findings[0], "meeting-date-matches-path", lint.Error, "Meetings/Snyk/2026/07/15/1000 - Differs.md")
}

func TestActionItemsCountDriftCountsCheckedAndUncheckedTasksAtAnyIndent(t *testing.T) {
	t.Parallel()

	body := strings.Join([]string{
		"- [ ] top-level",
		"  - [x] nested",
		"      - [ ] deeply nested",
		"- ordinary bullet",
		"[[Missing Link That Another Rule Would Report]]",
	}, "\n")

	t.Run("matching count", func(t *testing.T) {
		findings := runOnly(t, map[string]string{
			"Action Items.md": "---\ntotal_items: 3\n---\n" + body + "\n",
		}, "action-items-count-drift")
		require.Empty(t, findings)
	})

	t.Run("drifting count", func(t *testing.T) {
		findings := runOnly(t, map[string]string{
			"Action Items.md": "---\ntotal_items: 2\n---\n" + body + "\n",
			"Other.md":        "---\ntotal_items: 0\n---\n- [ ] outside the named file\n",
		}, "action-items-count-drift")
		require.Len(t, findings, 1)
		requireFinding(t, findings[0], "action-items-count-drift", lint.Error, "Action Items.md")
		require.True(t, findings[0].Fixable)
	})
}

func TestLogEntryFormatAcceptsValidEntries(t *testing.T) {
	t.Parallel()

	findings := runOnly(t, map[string]string{
		"log.md": strings.Join([]string{
			"# Log",
			"## [2026-07-15] shipped-feature | First summary",
			"- first bullet",
			"Some prose is allowed.",
			"## [2024-02-29] noted | Leap-day summary",
			"- second bullet",
		}, "\n"),
		"Other.md": "## malformed heading outside log.md\n",
	}, "log-entry-format")
	require.Empty(t, findings)
}

func TestLogEntryFormatRejectsMalformedHeadingsInvalidDatesAndMissingBullets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		log  string
	}{
		{
			name: "uppercase action token",
			log:  "## [2026-07-15] Shipped | summary\n- bullet\n",
		},
		{
			name: "invalid calendar date",
			log:  "## [2026-02-30] shipped | summary\n- bullet\n",
		},
		{
			name: "missing separator and summary",
			log:  "## [2026-07-15] shipped\n- bullet\n",
		},
		{
			name: "entry has no bullet",
			log:  "## [2026-07-15] shipped | summary\nOnly prose.\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := runOnly(t, map[string]string{"log.md": tt.log}, "log-entry-format")
			require.Len(t, findings, 1)
			requireFinding(t, findings[0], "log-entry-format", lint.Error, "log.md")
		})
	}
}

func TestLogEntryFormatRequiresABulletInEachEntryBody(t *testing.T) {
	t.Parallel()

	findings := runOnly(t, map[string]string{
		"log.md": strings.Join([]string{
			"## [2026-07-16] first | no bullet in this entry",
			"Only prose.",
			"## [2026-07-15] second | has a bullet",
			"- bullet belongs to the second entry",
		}, "\n"),
	}, "log-entry-format")
	require.Len(t, findings, 1)
	requireFinding(t, findings[0], "log-entry-format", lint.Error, "log.md")
}

func TestLogNewestFirstAllowsEqualAndDescendingDates(t *testing.T) {
	t.Parallel()

	findings := runOnly(t, map[string]string{
		"log.md": strings.Join([]string{
			"## [2026-07-16] first | summary",
			"- bullet",
			"## [2026-07-16] second | summary",
			"- bullet",
			"## [2026-07-15] third | summary",
			"- bullet",
		}, "\n"),
	}, "log-newest-first")
	require.Empty(t, findings)
}

func TestLogNewestFirstRejectsDateGreaterThanEntryAbove(t *testing.T) {
	t.Parallel()

	findings := runOnly(t, map[string]string{
		"log.md": strings.Join([]string{
			"## [2026-07-15] older | summary",
			"- bullet",
			"## [2026-07-16] newer | summary",
			"- bullet",
		}, "\n"),
	}, "log-newest-first")
	require.Len(t, findings, 1)
	requireFinding(t, findings[0], "log-newest-first", lint.Error, "log.md")
}

func exactlyNLines(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = "line"
	}
	return strings.Join(lines, "\n")
}

func TestNowLineCapBoundariesAndSeverity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		lines    int
		severity lint.Severity
		has      bool
	}{
		{name: "exactly 60 is allowed", lines: 60},
		{name: "61 warns", lines: 61, severity: lint.Warning, has: true},
		{name: "exactly 80 warns", lines: 80, severity: lint.Warning, has: true},
		{name: "81 errors", lines: 81, severity: lint.Error, has: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := runOnly(t, map[string]string{"Now.md": exactlyNLines(tt.lines)}, "now-line-cap")
			if !tt.has {
				require.Empty(t, findings)
				return
			}
			require.Len(t, findings, 1)
			requireFinding(t, findings[0], "now-line-cap", tt.severity, "Now.md")
		})
	}
}

func TestWikilinkResolvesReportsBrokenBodyLinkAndSuggestionFixability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		target  string
		extra   map[string]string
		fixable bool
	}{
		{
			name:    "unique basename at distance one",
			target:  "Alphb",
			extra:   map[string]string{"Alpha.md": "# Alpha\n"},
			fixable: true,
		},
		{
			name:    "distance at least two",
			target:  "Zzzzz",
			extra:   map[string]string{"Alpha.md": "# Alpha\n"},
			fixable: false,
		},
		{
			name:   "multiple distance-one candidates",
			target: "Cut",
			extra: map[string]string{
				"Cat.md": "# Cat\n",
				"Cot.md": "# Cot\n",
			},
			fixable: false,
		},
		{
			name:    "unsafe opening bracket in target",
			target:  "Alph[a",
			extra:   map[string]string{"Alpha.md": "# Alpha\n"},
			fixable: false,
		},
		{
			name:    "unsafe closing bracket in target",
			target:  "Alph]a",
			extra:   map[string]string{"Alpha.md": "# Alpha\n"},
			fixable: false,
		},
		// ADJUDICATED (test wrong, spec §B): `|` and `#` are wikilink
		// syntax — they never end up inside the parsed target. These parse
		// as target "Alph" (+alias/fragment), which has a unique
		// distance-1 candidate "Alpha" and IS fixable.
		{
			name:    "alias delimiter is syntax, not target text",
			target:  "Alph|alias",
			extra:   map[string]string{"Alpha.md": "# Alpha\n"},
			fixable: true,
		},
		{
			name:    "fragment delimiter is syntax, not target text",
			target:  "Alph#fragment",
			extra:   map[string]string{"Alpha.md": "# Alpha\n"},
			fixable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"Source.md": "[[" + tt.target + "]]\n"}
			for path, contents := range tt.extra {
				files[path] = contents
			}
			findings := runOnly(t, files, "wikilink-resolves")
			require.Len(t, findings, 1)
			requireFinding(t, findings[0], "wikilink-resolves", lint.Error, "Source.md")
			require.Contains(t, strings.ToLower(findings[0].Message), "broken")
			require.Equal(t, tt.fixable, findings[0].Fixable)
		})
	}
}

func TestWikilinkResolvesChecksFragmentsAgainstTargetHeadings(t *testing.T) {
	t.Parallel()

	t.Run("existing heading", func(t *testing.T) {
		findings := runOnly(t, map[string]string{
			"Target.md": "# Existing Heading\n",
			"Source.md": "[[Target#Existing Heading]]\n",
		}, "wikilink-resolves")
		require.Empty(t, findings)
	})

	t.Run("missing heading", func(t *testing.T) {
		findings := runOnly(t, map[string]string{
			"Target.md": "# Existing Heading\n",
			"Source.md": "[[Target#Missing Heading]]\n",
		}, "wikilink-resolves")
		require.Len(t, findings, 1)
		requireFinding(t, findings[0], "wikilink-resolves", lint.Warning, "Source.md")
	})
}

// ADJUDICATED (test wrong; hermetic spec extract said "body wikilink" but
// the frozen catalog scopes wikilink-resolves to body + frontmatter):
// broken frontmatter links ARE findings; code-fence links are not.
func TestWikilinkResolvesIgnoresCodeFencesAndFrontmatterLinks(t *testing.T) {
	t.Parallel()

	findings := runOnly(t, map[string]string{
		"Source.md": strings.Join([]string{
			"---",
			"related: \"[[MissingFromFrontmatter]]\"",
			"---",
			"```",
			"[[MissingFromCodeFence]]",
			"```",
			"A valid body link: [[Target]].",
		}, "\n"),
		"Target.md": "# Target\n",
	}, "wikilink-resolves")
	require.Len(t, findings, 1)
	require.Contains(t, findings[0].Message, "MissingFromFrontmatter",
		"frontmatter links are in scope; code-fence links are not")
}

func TestOrphanNotesUsesProfileScopeAndInboundLinksFromOtherNotes(t *testing.T) {
	t.Parallel()

	findings := runOnly(t, map[string]string{
		"Projects/Snyk/Linked Project.md":       "body\n",
		"Projects/Personal/Orphan Project.md":   "body\n",
		"Resources/Snyk/Self Only.md":           "[[Self Only]]\n",
		"Resources/Personal/Linked Resource.md": "body\n",
		"People/Snyk/Outside Scope.md":          "body\n",
		"Projects/Other/Also Outside.md":        "body\n",
		"Inbox/Source.md": strings.Join([]string{
			"[[Linked Project]]",
			"[[Linked Resource]]",
		}, "\n"),
	}, "orphan-notes")

	require.Len(t, findings, 2)
	requireFinding(t, findings[0], "orphan-notes", lint.Warning, "Projects/Personal/Orphan Project.md")
	requireFinding(t, findings[1], "orphan-notes", lint.Warning, "Resources/Snyk/Self Only.md")
}

func TestLintRunIsDeterministicSortedAndNilOnlyRunsRules(t *testing.T) {
	t.Parallel()

	ix, prof := buildLintVault(t, map[string]string{
		"Action Items.md":         "---\ntotal_items: 0\n---\n- [ ] one\n",
		"Now.md":                  exactlyNLines(61),
		"Source.md":               "[[Missing Target]]\n",
		"Unclosed.md":             "---\ntitle: unclosed\n",
		"Projects/Snyk/Orphan.md": "body\n",
	})

	first, err := lint.Run(ix, prof, nil, nil)
	require.NoError(t, err)
	second, err := lint.Run(ix, prof, nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, first)
	require.Equal(t, first, second)

	require.True(t, sort.SliceIsSorted(first, func(i, j int) bool {
		a, b := first[i], first[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.Message < b.Message
	}))
}
