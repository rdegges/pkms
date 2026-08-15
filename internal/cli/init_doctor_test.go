package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/config"
)

// testEnv isolates config + state in temp dirs.
func testEnv(t *testing.T) (configPath string) {
	t.Helper()
	tmp := t.TempDir()
	configPath = filepath.Join(tmp, "config.toml")
	t.Setenv("PKMS_CONFIG", configPath)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "cfg"))
	return configPath
}

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

func TestInitScaffoldsParaVault(t *testing.T) {
	cfgPath := testEnv(t)
	vault := filepath.Join(t.TempDir(), "My Vault")

	out, err := runCLI(t, "init", "--path", vault)
	require.NoError(t, err, out)

	for _, dir := range []string{"Projects", "Areas", "Resources", "Archive", ".git"} {
		st, err := os.Stat(filepath.Join(vault, dir))
		require.NoError(t, err, dir)
		require.True(t, st.IsDir())
	}
	gi, err := os.ReadFile(filepath.Join(vault, ".gitignore"))
	require.NoError(t, err)
	require.Contains(t, string(gi), ".obsidian/workspace*")

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Len(t, cfg.Vaults, 1)
	require.Equal(t, "my-vault", cfg.Vaults[0].Name, "name slugified from basename")
	require.Equal(t, "para", cfg.Vaults[0].Profile)

	// Idempotent: run again, nothing breaks, no duplicate registration.
	out, err = runCLI(t, "init", "--path", vault)
	require.NoError(t, err, out)
	cfg, _ = config.Load(cfgPath)
	require.Len(t, cfg.Vaults, 1)

	// Doctor is happy (warns at most; no failures).
	out, err = runCLI(t, "doctor", "--json")
	require.NoError(t, err, out)
	var report struct {
		Checks []checkResult `json:"checks"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	for _, c := range report.Checks {
		require.NotEqual(t, "fail", c.Status, "%+v", c)
	}
}

func TestInitRefusesNonEmptyWithoutAdopt(t *testing.T) {
	testEnv(t)
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "existing.md"), []byte("x"), 0o644))

	_, err := runCLI(t, "init", "--path", vault)
	require.ErrorContains(t, err, "--adopt")

	out, err := runCLI(t, "init", "--path", vault, "--adopt", "--name", "adopted")
	require.NoError(t, err, out)

	// Adopt never scaffolds folders over existing content.
	_, statErr := os.Stat(filepath.Join(vault, "Projects"))
	require.True(t, os.IsNotExist(statErr), "adopt must not scaffold")
	// But the content is preserved and the repo exists.
	_, err = os.Stat(filepath.Join(vault, "existing.md"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(vault, ".git"))
	require.NoError(t, err)
}

func TestInitDryRunChangesNothing(t *testing.T) {
	cfgPath := testEnv(t)
	vault := filepath.Join(t.TempDir(), "v")

	out, err := runCLI(t, "init", "--path", vault, "--dry-run")
	require.NoError(t, err)
	require.Contains(t, out, "plan")

	_, statErr := os.Stat(vault)
	require.True(t, os.IsNotExist(statErr))
	_, statErr = os.Stat(cfgPath)
	require.True(t, os.IsNotExist(statErr), "config untouched on dry-run")
}

func TestInitRejectsNameCollision(t *testing.T) {
	testEnv(t)
	v1 := filepath.Join(t.TempDir(), "vault")
	v2 := filepath.Join(t.TempDir(), "vault")

	_, err := runCLI(t, "init", "--path", v1)
	require.NoError(t, err)
	_, err = runCLI(t, "init", "--path", v2)
	require.ErrorContains(t, err, "already registered")
}

func TestDoctorFailsOnMissingVaultPath(t *testing.T) {
	cfgPath := testEnv(t)
	require.NoError(t, config.AppendVault(cfgPath, config.Vault{
		Name: "ghost", Path: "/nonexistent/vault", Profile: "para",
	}))
	out, err := runCLI(t, "doctor")
	require.ErrorIs(t, err, errFindings, out)
}

// SPEC §31.9 gate discipline: the asset-refs check counts as installed
// only once it is OBSERVED rejecting a seeded dangling reference. This
// test seeds one and asserts the warning; without the check it would pass
// silently (green-by-omission), so the assertion is the gate.
func TestDoctorAssetRefsRejectsDanglingInVaultRef(t *testing.T) {
	cfgPath := testEnv(t)
	vaultPath := filepath.Join(t.TempDir(), "vault")
	_, err := runCLI(t, "init", "--path", vaultPath)
	require.NoError(t, err)
	require.NoError(t, config.AppendVault(cfgPath, config.Vault{
		Name: "seed", Path: vaultPath, Profile: "para",
	}))

	// A note whose assets: ledger points at a file that does not exist.
	note := "---\n" +
		"title: Clip\n" +
		"assets:\n" +
		"  - Attachments/ghost.pdf\n" +
		"---\n\n## Attachments\n\n- ![[Attachments/ghost.pdf]]\n"
	require.NoError(t, os.WriteFile(filepath.Join(vaultPath, "_Inbox", "note.md"), []byte(note), 0o644))

	out, err := runCLI(t, "doctor")
	require.NoError(t, err, "a dangling in-vault ref is a warning, not a failure: %s", out)
	require.Contains(t, out, "asset-refs")
	require.Contains(t, out, "moved or deleted")

	// A present attachment makes the check green.
	require.NoError(t, os.MkdirAll(filepath.Join(vaultPath, "Attachments"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vaultPath, "Attachments", "ghost.pdf"), []byte("%PDF"), 0o644))
	out, err = runCLI(t, "doctor")
	require.NoError(t, err)
	require.Contains(t, out, "every in-vault attachment exists")
	require.NotContains(t, out, "moved or deleted")
}

// An external (absolute) asset path absent on this machine is INFO, never a
// warning — it is expected absent on other synced devices (§31.9).
func TestDoctorAssetRefsExternalPathIsInfoNotWarning(t *testing.T) {
	cfgPath := testEnv(t)
	vaultPath := filepath.Join(t.TempDir(), "vault")
	_, err := runCLI(t, "init", "--path", vaultPath)
	require.NoError(t, err)
	require.NoError(t, config.AppendVault(cfgPath, config.Vault{
		Name: "seed", Path: vaultPath, Profile: "para",
	}))
	note := "---\ntitle: Big\nassets:\n  - /nonexistent/store/deadbeef.mp4\n---\n\nbody\n"
	require.NoError(t, os.WriteFile(filepath.Join(vaultPath, "_Inbox", "big.md"), []byte(note), 0o644))

	out, err := runCLI(t, "doctor")
	require.NoError(t, err, "an absent external path never fails doctor: %s", out)
	require.Contains(t, out, "expected on other synced devices")
	require.NotContains(t, out, "moved or deleted", "external absence is not an in-vault dangle")
}

// Internal-consistency invariant: every per-vault doctor check must
// identify its vault by the SAME label. All other per-vault checks
// (vault-path, vault-writable, profile, vault-git, quarantine) use the
// configured `v.Name`; asset-refs uses `filepath.Base(v.Path)`. When the
// vault dir basename differs from the configured name (init slugifies, so
// any dir with capitals/spaces triggers this), asset-refs is mislabeled —
// a JSON consumer grouping checks by the `vault` field mis-attributes the
// asset-refs row. This test fails until asset-refs uses v.Name like the
// rest. (Defect proof: policy-independent internal-consistency invariant.)
func TestDoctorAssetRefsLabelMatchesOtherChecks(t *testing.T) {
	testEnv(t)
	// Basename "My Vault" -> init registers the slug "my-vault", so the
	// configured name and the path basename deliberately diverge.
	vaultPath := filepath.Join(t.TempDir(), "My Vault")
	_, err := runCLI(t, "init", "--path", vaultPath)
	require.NoError(t, err)

	out, err := runCLI(t, "doctor", "--json")
	require.NoError(t, err, out)

	var report struct {
		Checks []struct {
			Name  string `json:"name"`
			Vault string `json:"vault"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))

	labels := map[string]string{}
	for _, c := range report.Checks {
		if c.Vault != "" {
			labels[c.Name] = c.Vault
		}
	}
	require.NotEmpty(t, labels["vault-path"], "sanity: vault-path is a per-vault check")
	require.Equal(t, labels["vault-path"], labels["asset-refs"],
		"asset-refs must label its vault the same way every other per-vault check does")
}

// The warning aggregates a count and cites one example. Two dangling
// in-vault refs must surface as "2 in-vault attachment(s)".
func TestDoctorAssetRefsCountsMultipleDangling(t *testing.T) {
	testEnv(t)
	vaultPath := filepath.Join(t.TempDir(), "vault")
	_, err := runCLI(t, "init", "--path", vaultPath)
	require.NoError(t, err)

	a := "---\ntitle: A\nassets:\n  - Attachments/a.pdf\n---\n\nbody\n"
	b := "---\ntitle: B\nassets:\n  - Attachments/b.pdf\n---\n\nbody\n"
	require.NoError(t, os.WriteFile(filepath.Join(vaultPath, "_Inbox", "a.md"), []byte(a), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(vaultPath, "_Inbox", "b.md"), []byte(b), 0o644))

	out, err := runCLI(t, "doctor")
	require.NoError(t, err, out)
	require.Contains(t, out, "2 in-vault attachment(s) moved or deleted")
	require.Contains(t, out, "e.g.", "the warning cites one example path")
}

// Malformed / hand-edited `assets:` ledgers must never crash doctor and
// must not false-warn. The check only understands a list of strings, so a
// scalar `assets:` and non-string list entries are silently skipped.
// NOTE (limitation): a scalar `assets:` with a dangling path escapes the
// check entirely — see implementation commentary.
func TestDoctorAssetRefsMalformedLedgerIgnored(t *testing.T) {
	testEnv(t)
	vaultPath := filepath.Join(t.TempDir(), "vault")
	_, err := runCLI(t, "init", "--path", vaultPath)
	require.NoError(t, err)

	// List with an empty string and a non-string entry — both skipped.
	mixed := "---\ntitle: Mixed\nassets:\n  - \"\"\n  - 42\n---\n\nbody\n"
	// A note with no frontmatter at all — must be skipped, not crash.
	plain := "# Just a heading\n\nno frontmatter here\n"
	require.NoError(t, os.WriteFile(filepath.Join(vaultPath, "_Inbox", "mixed.md"), []byte(mixed), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(vaultPath, "_Inbox", "plain.md"), []byte(plain), 0o644))

	out, err := runCLI(t, "doctor")
	require.NoError(t, err, out)
	require.Contains(t, out, "every in-vault attachment exists",
		"empty/non-string entries and no-frontmatter notes are skipped, not false-warned")
	require.NotContains(t, out, "moved or deleted")
}

// A hand-edited SCALAR assets: value is a legitimate one-entry ledger, so a
// dangling scalar path must warn — never silent-green (gates fail closed).
func TestDoctorAssetRefsScalarDanglingWarns(t *testing.T) {
	testEnv(t)
	vaultPath := filepath.Join(t.TempDir(), "vault")
	_, err := runCLI(t, "init", "--path", vaultPath)
	require.NoError(t, err)

	scalar := "---\ntitle: Scalar\nassets: Attachments/missing.pdf\n---\n\nbody\n"
	require.NoError(t, os.WriteFile(filepath.Join(vaultPath, "_Inbox", "scalar.md"), []byte(scalar), 0o644))

	out, err := runCLI(t, "doctor")
	require.NoError(t, err, "a dangling scalar ref is a warning, not a failure: %s", out)
	require.Contains(t, out, "moved or deleted", "a scalar ledger is checked like a one-entry list")
}

// The new `info` status must plumb through --json: an absent external path
// yields a check with status "info" and increments the info summary count.
func TestDoctorAssetRefsInfoStatusInJSON(t *testing.T) {
	testEnv(t)
	vaultPath := filepath.Join(t.TempDir(), "vault")
	_, err := runCLI(t, "init", "--path", vaultPath)
	require.NoError(t, err)
	note := "---\ntitle: Big\nassets:\n  - /nonexistent/store/deadbeef.mp4\n---\n\nbody\n"
	require.NoError(t, os.WriteFile(filepath.Join(vaultPath, "_Inbox", "big.md"), []byte(note), 0o644))

	out, err := runCLI(t, "doctor", "--json")
	require.NoError(t, err, out)

	var report struct {
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
		Summary map[string]int `json:"summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))

	var found bool
	for _, c := range report.Checks {
		if c.Name == "asset-refs" && c.Status == "info" {
			found = true
		}
	}
	require.True(t, found, "expected an asset-refs check with status \"info\": %s", out)
	require.GreaterOrEqual(t, report.Summary["info"], 1, "info summary count must include the external path")
}

// A note with control bytes must FAIL doctor, not warn (§33): a vault that
// reports healthy must be safe for machine reads and edits.
func TestDoctorNoteTextFailsOnControlBytes(t *testing.T) {
	testEnv(t)
	vaultPath := filepath.Join(t.TempDir(), "vault")
	_, err := runCLI(t, "init", "--path", vaultPath)
	require.NoError(t, err)

	note := "---\ntitle: Corrupt\n---\n\nbefore\x00after\n"
	require.NoError(t, os.WriteFile(filepath.Join(vaultPath, "_Inbox", "corrupt.md"), []byte(note), 0o644))

	out, err := runCLI(t, "doctor")
	require.Error(t, err, "a control byte in a note must fail doctor: %s", out)
	require.Contains(t, out, "note-text")
	require.Contains(t, out, "_Inbox/corrupt.md")

	// Repairing the note turns the check green.
	require.NoError(t, os.WriteFile(filepath.Join(vaultPath, "_Inbox", "corrupt.md"), []byte("---\ntitle: Clean\n---\n\nbefore after\n"), 0o644))
	out, err = runCLI(t, "doctor")
	require.NoError(t, err, out)
	require.Contains(t, out, "every note is valid text")
}

func TestDoctorNoteTextFailsOnInvalidUTF8(t *testing.T) {
	testEnv(t)
	vaultPath := filepath.Join(t.TempDir(), "vault")
	_, err := runCLI(t, "init", "--path", vaultPath)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(vaultPath, "_Inbox", "latin1.md"), []byte("caf\xe9\n"), 0o644))

	out, err := runCLI(t, "doctor")
	require.Error(t, err, "invalid UTF-8 in a note must fail doctor: %s", out)
	require.Contains(t, out, "note-text")
}

func TestProfileListAndEject(t *testing.T) {
	testEnv(t)
	out, err := runCLI(t, "profile", "list")
	require.NoError(t, err)
	require.Contains(t, out, "para")
	require.Contains(t, out, "rdegges")

	dest := filepath.Join(t.TempDir(), "ejected")
	require.NoError(t, os.MkdirAll(dest, 0o755))
	_, err = runCLI(t, "profile", "eject", "rdegges", dest)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dest, "schemas", "person.schema.json"))
	require.NoError(t, err)
}
