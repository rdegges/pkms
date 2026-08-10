package rules_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

// runWithProfile lints a synthetic vault under a named built-in profile.
// The stock helper is pinned to `rdegges`; asset notes must lint clean under
// BOTH built-ins, whose attachments dirs differ ("+" vs "Attachments").
func runWithProfile(t *testing.T, profName string, files map[string]string, only ...string) []lint.Finding {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	prof, err := profile.Load(profName)
	require.NoError(t, err)
	ix, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	require.NoError(t, err)
	fs, err := lint.Run(ix, prof, nil, only)
	require.NoError(t, err)
	return fs
}

// assetNote is the body/frontmatter shape the ingest pipeline emits for a
// stored in-vault asset (SPEC §31.4). The filename form matches the asset
// type's `{{tsname .created}} - {{.title}}` template.
const assetNoteName = "2026-08-09T120000-0700 - report.md"

func assetNote(attachmentsDir, filename string) string {
	return `---
title: report
source: file:///home/u/report.pdf
created: 2026-08-09T12:00:00-07:00
tags:
  - asset
mime: application/pdf
size: 1234
sha256: 3a7bd3e2360a3d29eea436fcfb7e44c735d117c42d1c1835420b6b9942dd4f1b
assets:
  - ` + attachmentsDir + `/` + filename + `
---

## Attachments

- ![[` + attachmentsDir + `/` + filename + `]]
`
}

// The regression this pins: SPEC §31.9 asserts "embeds resolve through the
// same link index", and §31.10 is the record of exactly this class of bug —
// an ingest contract that makes every ingested note lint-error. If the
// attachment embed does not resolve, `pkms lint` fails on every asset the
// user captures.
func TestAssetEmbedResolvesUnderBothBuiltinProfiles(t *testing.T) {
	cases := []struct {
		prof, notePath, attachments string
	}{
		{"rdegges", "Resources/Clips/Inbox/" + assetNoteName, "+"},
		{"para", "_Inbox/" + assetNoteName, "Attachments"},
	}
	for _, tc := range cases {
		t.Run(tc.prof, func(t *testing.T) {
			fs := runWithProfile(t, tc.prof, map[string]string{
				tc.notePath:                    assetNote(tc.attachments, "report.pdf"),
				tc.attachments + "/report.pdf": "%PDF-1.5 bytes",
			})
			require.Empty(t, fs, "an ingested asset note must lint clean out of the box: %+v", fs)
		})
	}
}

// The other half of the gate: a dangling embed must still be caught, so the
// clean result above is not clean-by-omission.
func TestAssetEmbedDanglingIsStillReported(t *testing.T) {
	fs := runWithProfile(t, "para", map[string]string{
		"_Inbox/" + assetNoteName: assetNote("Attachments", "report.pdf"),
		// no Attachments/report.pdf on disk
	}, "no-broken-embed")
	m := byRule(fs)
	require.Len(t, m["no-broken-embed"], 1, "a missing attachment is a broken embed: %+v", fs)
	require.Contains(t, m["no-broken-embed"][0].Message, "Attachments/report.pdf")
}

// Duplicate basenames across folders are why the pipeline writes
// PATH-QUALIFIED embeds (SPEC §5/§31.4): a bare [[report.pdf]] would be
// ambiguous, the qualified form must not be.
func TestAssetEmbedIsPathQualifiedAgainstDuplicateBasenames(t *testing.T) {
	fs := runWithProfile(t, "para", map[string]string{
		"_Inbox/" + assetNoteName: assetNote("Attachments", "report.pdf"),
		"Attachments/report.pdf":  "%PDF-1.5 one",
		"Archive/report.pdf":      "%PDF-1.5 two",
	}, "no-broken-embed", "wikilink-ambiguous")
	require.Empty(t, fs, "the path-qualified embed picks exactly one file: %+v", fs)
}

// An asset filename containing spaces and unicode (common for real
// attachments) must still resolve.
func TestAssetEmbedWithSpacesAndUnicode(t *testing.T) {
	name := "café statement 2026.pdf"
	fs := runWithProfile(t, "para", map[string]string{
		"_Inbox/" + assetNoteName: assetNote("Attachments", name),
		"Attachments/" + name:     "%PDF-1.5",
	}, "no-broken-embed")
	require.Empty(t, fs, "spaces and unicode in an attachment name must not break the embed: %+v", fs)
}

// Files under the attachments dir are indexed but never parsed as notes: a
// markdown attachment must not become a lintable note (and must not trip
// frontmatter rules).
func TestMarkdownAttachmentIsNotLintedAsANote(t *testing.T) {
	fs := runWithProfile(t, "para", map[string]string{
		"Attachments/emailed-notes.md": "no frontmatter, TODO placeholder, [[Nonexistent]]\n",
	})
	require.Empty(t, fs, "an attachment is a file, not a note: %+v", fs)
}
