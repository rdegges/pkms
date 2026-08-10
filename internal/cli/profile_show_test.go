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

// §32.2: each schema is inlined BYTE-FAITHFULLY — the bytes the agent
// validates against must equal the bytes the writer enforces, never a
// re-marshal.
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
	cfgPath := testEnv(t)
	vaultPath := filepath.Join(t.TempDir(), "v")
	_, err := runCLI(t, "init", "--path", vaultPath)
	require.NoError(t, err)
	_ = cfgPath

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
