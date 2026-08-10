package cli

import (
	"bytes"
	"encoding/json"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/query"
	"github.com/rdegges/pkms/internal/vault"
)

// This file holds the single source of the read-surface JSON shapes
// (query, lint, profile show). The CLI --json paths and the `pkms mcp`
// server (§32.6) both marshal through these functions — there is no
// parallel serialization, so the two can never drift.

// encodeJSON marshals v the way the CLI's json.Encoder does: two-space
// indent and a trailing newline. escapeHTML mirrors each surface's
// existing behavior — off for profile show so inlined schema bytes survive
// verbatim (§32.2), on for query/lint (their historical output).
func encodeJSON(v any, escapeHTML bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// queryJSON is the frozen `query --json` shape: {"results": [...]} with a
// stable empty array, never null (SPEC §10).
func queryJSON(ix *vault.Index, prof *profile.Profile, opts query.Options) ([]byte, error) {
	results := query.Run(ix, prof, opts)
	if results == nil {
		results = []query.Result{}
	}
	return encodeJSON(map[string]any{"results": results}, true)
}

// lintPayload is the frozen `lint --json` object: {"vault", "findings",
// "summary":{error,warning}} with a stable empty array (SPEC §8). Both the
// CLI --json path and the MCP lint tool build their bytes from this.
func lintPayload(vaultName string, findings []lint.Finding) map[string]any {
	if findings == nil {
		findings = []lint.Finding{}
	}
	summary := map[string]int{"error": 0, "warning": 0}
	for _, f := range findings {
		summary[string(f.Severity)]++
	}
	return map[string]any{"vault": vaultName, "findings": findings, "summary": summary}
}

// lintJSON runs the rule engine read-only (no --fix) and serializes it —
// the MCP lint tool's whole job (§32.6).
func lintJSON(vaultName string, ix *vault.Index, prof *profile.Profile,
	lintCfg map[string]map[string]any, only []string) ([]byte, error) {
	findings, err := lint.Run(ix, prof, lintCfg, only)
	if err != nil {
		return nil, err
	}
	return encodeJSON(lintPayload(vaultName, findings), true)
}

// profileJSON is the frozen `profile show --json` shape (§32.2), HTML
// escaping OFF so inlined JSON Schemas survive byte-for-byte.
func profileJSON(prof *profile.Profile) ([]byte, error) {
	view, err := buildProfileView(prof)
	if err != nil {
		return nil, err
	}
	return encodeJSON(view, false)
}
