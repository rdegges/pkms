package rules_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
)

// #31 routes EVERY list-valued option through lint.CfgStrings.
// TestNonListConfigValueFailsTheRunForEveryShape covers every wrong shape on
// one key; this covers every key with one wrong shape. Together they are the
// grid. Without it, the secondary list keys (`allowlist`, `underscore_exempt`,
// `extra_allowed`, ...) had their new error branches unexercised — measured
// as uncovered blocks at naming.go:26/43/67/117 and vaultwide.go:86/117.
//
// Each case sets any key the factory reads FIRST to a valid value, so the
// factory reaches the key under test instead of short-circuiting on
// "unconfigured".
func TestEveryListValuedKeyRejectsAScalar(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, tc := range []struct {
		rule string
		key  string
		with map[string]any // other keys needed to reach the one under test
	}{
		{rule: "frontmatter-present", key: "warning_types"},
		{rule: "frontmatter-schema", key: "warning_types"},
		{rule: "date-format-iso", key: "keys"},
		{rule: "related-projects-resolve", key: "folders"},
		{rule: "orphan-notes", key: "scopes"},
		{rule: "non-markdown-in-note-folders", key: "scopes"},
		{rule: "person-required-sections", key: "sections"},
		{rule: "meeting-required-sections", key: "sections"},
		{rule: "project-status-vocab", key: "allowlist"},
		{rule: "meeting-filename-format", key: "extra_allowed"},
		{rule: "no-junk-files", key: "patterns"},
		{rule: "no-junk-files", key: "underscore_exempt", with: map[string]any{"patterns": []any{"*.bak"}}},
		{rule: "index-no-inventory", key: "forbidden_prefixes", with: map[string]any{"file": "index.md"}},
		{rule: "log-action-vocab", key: "allowlist", with: map[string]any{"file": "log.md"}},
		{rule: "now-fixed-sections", key: "sections", with: map[string]any{"file": "Now.md"}},
		{rule: "root-canonical-only", key: "files"},
		{rule: "root-canonical-only", key: "allowlist", with: map[string]any{"files": []any{"Now.md"}}},
		{rule: "top-level-folders-fixed", key: "dirs"},
		{rule: "top-level-folders-fixed", key: "allowlist", with: map[string]any{"dirs": []any{"Areas"}}},
		// domain-split-folders reads every key that is not enabled/severity.
		{rule: "domain-split-folders", key: "Areas"},
		// cfgRegexps now reads through CfgStrings too, so the regex-valued
		// `patterns` keys inherit the same contract.
		{rule: "no-per-run-notes", key: "patterns"},
		{rule: "no-drafts-folder", key: "patterns"},
		{rule: "now-no-sync-sections", key: "patterns", with: map[string]any{"file": "Now.md"}},
	} {
		t.Run(tc.rule+"."+tc.key, func(t *testing.T) {
			cfg := map[string]any{}
			for k, v := range tc.with {
				cfg[k] = v
			}
			cfg[tc.key] = "a-bare-scalar"
			_, err := lint.Run(ix, prof,
				map[string]map[string]any{tc.rule: cfg}, []string{tc.rule})
			require.Errorf(t, err,
				"%s.%s as a bare scalar must fail the run, not silently disable the rule",
				tc.rule, tc.key)
			require.Contains(t, err.Error(), tc.key, "the error must name the key")
			require.Contains(t, err.Error(), tc.rule, "the error must name the rule")
		})
	}
}

// The same grid for a list holding a non-string entry — the other half of
// CfgStrings' contract, and the shape a hand-merged vault override produces.
func TestEveryListValuedKeyRejectsANonStringEntry(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, tc := range []struct {
		rule string
		key  string
		with map[string]any
	}{
		{rule: "frontmatter-present", key: "warning_types"},
		{rule: "date-format-iso", key: "keys"},
		{rule: "orphan-notes", key: "scopes"},
		{rule: "person-required-sections", key: "sections"},
		{rule: "no-junk-files", key: "underscore_exempt", with: map[string]any{"patterns": []any{"*.bak"}}},
		{rule: "root-canonical-only", key: "allowlist", with: map[string]any{"files": []any{"Now.md"}}},
		{rule: "top-level-folders-fixed", key: "allowlist", with: map[string]any{"dirs": []any{"Areas"}}},
		{rule: "meeting-filename-format", key: "extra_allowed"},
		{rule: "log-action-vocab", key: "allowlist", with: map[string]any{"file": "log.md"}},
		{rule: "now-fixed-sections", key: "sections", with: map[string]any{"file": "Now.md"}},
	} {
		t.Run(tc.rule+"."+tc.key, func(t *testing.T) {
			cfg := map[string]any{}
			for k, v := range tc.with {
				cfg[k] = v
			}
			cfg[tc.key] = []any{"fine", 7}
			_, err := lint.Run(ix, prof,
				map[string]map[string]any{tc.rule: cfg}, []string{tc.rule})
			require.Errorf(t, err, "%s.%s: a non-string entry must fail the run", tc.rule, tc.key)
			require.Contains(t, err.Error(), tc.key)
			require.Contains(t, err.Error(), tc.rule)
			require.Contains(t, err.Error(), "want string",
				"the message must say what was expected")
		})
	}
}

// Several factories read a PRIMARY key and return "not applicable" before
// they ever reach a secondary list key, so in principle a malformed secondary
// key could escape validation. In practice it cannot on the shipped profiles:
// overrides are merged onto the profile's own rule config, which supplies the
// primary key, so the factory always reaches the secondary one. Pinned
// because it is the merge — not the factory — that closes the hole, and a
// change to either could reopen it silently.
func TestASecondaryListKeyIsValidatedThroughTheProfileMerge(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, tc := range []struct{ rule, key string }{
		{"root-canonical-only", "allowlist"},
		{"top-level-folders-fixed", "allowlist"},
		{"no-junk-files", "underscore_exempt"},
		{"log-action-vocab", "allowlist"},
		{"now-fixed-sections", "sections"},
	} {
		t.Run(tc.rule+"."+tc.key, func(t *testing.T) {
			// The override sets ONLY the secondary key; the primary comes
			// from the profile.
			_, err := lint.Run(ix, prof,
				map[string]map[string]any{tc.rule: {tc.key: "a-bare-scalar"}},
				[]string{tc.rule})
			require.Errorf(t, err,
				"an override naming only %s must still be validated", tc.key)
			require.Contains(t, err.Error(), tc.key)
		})
	}
}
