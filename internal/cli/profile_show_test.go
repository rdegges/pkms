package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// decodeProfileView runs `profile show --json` and decodes the frozen view.
func decodeProfileView(t *testing.T, args ...string) profileView {
	t.Helper()
	out, err := runCLI(t, append([]string{"profile", "show", "--json"}, args...)...)
	require.NoError(t, err, out)
	var v profileView
	require.NoError(t, json.Unmarshal([]byte(out), &v))
	return v
}

// §32.2 byte-faithfulness must survive schema bytes that Go's JSON encoder
// would otherwise HTML-escape (<, >, &). Built-in schemas contain none, so
// this seeds an ejected profile with such a schema and asserts the raw
// substring appears unescaped — the gate for the SetEscapeHTML(false) fix.
func TestProfileShowSchemaNotHTMLEscaped(t *testing.T) {
	testEnv(t)
	dest := filepath.Join(t.TempDir(), "custom")
	require.NoError(t, os.MkdirAll(dest, 0o755))
	_, err := runCLI(t, "profile", "eject", "para", dest)
	require.NoError(t, err)

	schemaPath := filepath.Join(dest, "schemas", "clip.schema.json")
	raw, err := os.ReadFile(schemaPath)
	require.NoError(t, err)
	// Insert a valid extra property whose description carries all three
	// HTML-escapable characters (<, >, &), right after "properties": {.
	injected := `"properties": {` + "\n" +
		`    "note": { "type": "string", "description": "a<b & c>d" },`
	mutated := bytes.Replace(raw, []byte(`"properties": {`), []byte(injected), 1)
	require.NotEqual(t, raw, mutated, "fixture injection must apply")
	require.NoError(t, os.WriteFile(schemaPath, mutated, 0o644))

	out, err := runCLI(t, "profile", "show", "--json", dest)
	require.NoError(t, err, out)
	// The raw characters survive verbatim...
	require.Contains(t, out, `a<b & c>d`, "schema bytes survive verbatim, not HTML-escaped")
	// ...and Go's HTML-entity \u escapes never appear (that's the bug the
	// SetEscapeHTML(false) fix prevents — the chars themselves SHOULD be
	// present, so assert on the escape sequences, not the bare runes).
	require.NotContains(t, out, "\\u003c", "< must not be escaped to a \\u sequence")
	require.NotContains(t, out, "\\u003e", "> must not be escaped to a \\u sequence")
	require.NotContains(t, out, "\\u0026", "& must not be escaped to a \\u sequence")
}

func TestProfileShowByName(t *testing.T) {
	testEnv(t)
	v := decodeProfileView(t, "para")
	require.Equal(t, "para", v.Name)
	require.Equal(t, 1, v.SchemaVersion)
	require.Equal(t, "Attachments", v.Attachments)
	require.Contains(t, v.Scaffold, "_Inbox")
	require.Equal(t, "clip", v.Ingest.Clip)
	require.Equal(t, "asset", v.Ingest.Asset)
}

// §32.2/§4: types must appear in classification order — asset before clip,
// because both scope _Inbox/*.md and the sha256-triggered asset wins first.
func TestProfileShowTypesAreInClassificationOrder(t *testing.T) {
	testEnv(t)
	v := decodeProfileView(t, "para")
	require.GreaterOrEqual(t, len(v.Types), 2)
	require.Equal(t, "asset", v.Types[0].Name, "content-triggered type precedes the fallback")
	require.Equal(t, "clip", v.Types[1].Name)
	require.Equal(t, []string{"sha256"}, v.Types[0].RequireAnyKey)
}

// §32.2: each schema is inlined without re-marshaling — key order and
// every token preserved verbatim, only indentation whitespace re-flowed by
// the encoder. So the agent validates against exactly what the writer
// enforces.
func TestProfileShowSchemaIsByteFaithful(t *testing.T) {
	testEnv(t)
	out, err := runCLI(t, "profile", "show", "--json", "para")
	require.NoError(t, err, out)
	var v profileView
	require.NoError(t, json.Unmarshal([]byte(out), &v))

	var assetType *typeView
	for i := range v.Types {
		if v.Types[i].Name == "asset" {
			assetType = &v.Types[i]
		}
	}
	require.NotNil(t, assetType)
	require.NotNil(t, assetType.Schema, "asset type declares a schema")

	// Byte-faithful means the schema is inlined, not re-marshaled: the JSON
	// encoder re-indents an embedded RawMessage's whitespace, but it never
	// re-tokenizes it — so key ORDER is preserved. (A Go-map round-trip
	// would alphabetize keys.) Compacting both erases whitespace while
	// keeping order, so compacted equality proves no re-marshal occurred.
	want, err := os.ReadFile(filepath.FromSlash("../../profiles/para/schemas/asset.schema.json"))
	require.NoError(t, err)
	var wantC, gotC bytes.Buffer
	require.NoError(t, json.Compact(&wantC, want))
	require.NoError(t, json.Compact(&gotC, assetType.Schema))
	require.Equal(t, wantC.String(), gotC.String(),
		"schema is the file's own bytes (key order preserved), not a re-marshal")
}

func TestProfileShowByVault(t *testing.T) {
	testEnv(t)
	vaultPath := filepath.Join(t.TempDir(), "v")
	_, err := runCLI(t, "init", "--path", vaultPath)
	require.NoError(t, err)

	// Single vault: --vault resolves to its profile.
	v := decodeProfileView(t, "--vault", "v")
	require.Equal(t, "para", v.Name)
}

func TestProfileShowNameAndVaultConflict(t *testing.T) {
	testEnv(t)
	_, err := runCLI(t, "profile", "show", "para", "--vault", "v")
	require.ErrorContains(t, err, "OR --vault")
}

func TestProfileShowEjectedCustomProfile(t *testing.T) {
	testEnv(t)
	dest := filepath.Join(t.TempDir(), "custom")
	require.NoError(t, os.MkdirAll(dest, 0o755))
	_, err := runCLI(t, "profile", "eject", "para", dest)
	require.NoError(t, err)

	v := decodeProfileView(t, dest)
	require.Equal(t, "para", v.Name, "an ejected profile shows by its path")
	require.Contains(t, v.Scaffold, "_Inbox")
}

func TestProfileShowUnknownProfileErrors(t *testing.T) {
	testEnv(t)
	_, err := runCLI(t, "profile", "show", "nope-not-a-profile")
	require.Error(t, err)
}

func TestProfileShowHumanOutput(t *testing.T) {
	testEnv(t)
	out, err := runCLI(t, "profile", "show", "para")
	require.NoError(t, err, out)
	require.Contains(t, out, "para — PARA")
	require.Contains(t, out, "classification order")
	require.Contains(t, out, "ingest: clip=clip asset=asset")
}

// The rdegges profile has index rules and templated types — exercise the
// index rendering and a type that carries a template.
func TestProfileShowRdeggesHasIndexes(t *testing.T) {
	testEnv(t)
	v := decodeProfileView(t, "rdegges")
	require.NotEmpty(t, v.Indexes, "rdegges declares index rules")
	require.NotEmpty(t, v.Types)
}

// Human output on rdegges exercises the branches para can't: the indexes
// section (para declares none) and the "no schema" per-type marker (para's
// two types both carry schemas). Also pins that a non-empty attachments dir
// renders literally rather than as "(none)".
func TestProfileShowHumanOutputRdegges(t *testing.T) {
	testEnv(t)
	out, err := runCLI(t, "profile", "show", "rdegges")
	require.NoError(t, err, out)

	require.Contains(t, out, "attachments: +", "non-empty attachments renders verbatim")
	require.Contains(t, out, "\nindexes:\n", "profiles with index rules render the section")
	require.Contains(t, out, "Projects.md lists Projects/{Snyk,Personal}/*.md (must-link-all)")
	// canonical-root declares no schema — the human marker must say so.
	require.Regexp(t, `canonical-root\s+folder=\s+no schema`, out)
}

// §32.2: the view's Schema field is JSON null for a type that declares no
// schema, and the inlined bytes for one that does. The struct documents this
// null contract; decoding a null RawMessage yields the literal bytes "null".
func TestProfileShowJSONSchemaNullForTypelessType(t *testing.T) {
	testEnv(t)
	v := decodeProfileView(t, "rdegges")

	byName := map[string]typeView{}
	for _, ty := range v.Types {
		byName[ty.Name] = ty
	}

	canonical, ok := byName["canonical-root"]
	require.True(t, ok, "rdegges declares canonical-root")
	require.Equal(t, "null", string(canonical.Schema),
		"a schema-less type emits JSON null, not {} and not omitted")

	person, ok := byName["person"]
	require.True(t, ok)
	require.NotEqual(t, "null", string(person.Schema), "person carries an inlined schema")
	require.NotEmpty(t, person.Schema)
	// Folder/filename placement templates are carried verbatim (the agent
	// reads placement from data, not assumption).
	require.Equal(t, "People/{{.category}}", person.Folder)
	require.Equal(t, "{{.name}}", person.Filename)
}

// §32.2: the frozen view surfaces the profile's lint config and index rules
// as data. Pin both — the ingest/agent seam depends on them being present.
func TestProfileShowJSONSurfacesLintAndIndexes(t *testing.T) {
	testEnv(t)

	para := decodeProfileView(t, "para")
	require.NotEmpty(t, para.Lint, "para declares lint config")
	require.Contains(t, para.Lint, "no-junk-files")

	rd := decodeProfileView(t, "rdegges")
	var projects *indexView
	for i := range rd.Indexes {
		if rd.Indexes[i].File == "Projects.md" {
			projects = &rd.Indexes[i]
		}
	}
	require.NotNil(t, projects, "rdegges declares a Projects.md index")
	require.Equal(t, "Projects/{Snyk,Personal}/*.md", projects.Lists)
	require.Equal(t, "must-link-all", projects.Policy)
}

// resolveShownProfile falls back to the configured vault when given neither a
// name nor --vault. Exercise the three config-resolution failure modes.
func TestProfileShowNoVaultConfigured(t *testing.T) {
	testEnv(t)
	_, err := runCLI(t, "profile", "show")
	require.ErrorContains(t, err, "no vaults configured")
}

func TestProfileShowMultipleVaultsAmbiguous(t *testing.T) {
	testEnv(t)
	_, err := runCLI(t, "init", "--path", filepath.Join(t.TempDir(), "a"))
	require.NoError(t, err)
	_, err = runCLI(t, "init", "--path", filepath.Join(t.TempDir(), "b"))
	require.NoError(t, err)

	_, err = runCLI(t, "profile", "show")
	require.ErrorContains(t, err, "multiple vaults configured")
}

func TestProfileShowUnknownVault(t *testing.T) {
	testEnv(t)
	_, err := runCLI(t, "init", "--path", filepath.Join(t.TempDir(), "v"))
	require.NoError(t, err)

	_, err = runCLI(t, "profile", "show", "--vault", "nope")
	require.ErrorContains(t, err, "unknown vault")
}
