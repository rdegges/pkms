package profile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// [[indexes]] declarations are validated at profile load (SPEC §36) so the
// layers that consume them — profile show today, the index lint rule later —
// can trust every entry. These are the red-first pins for that gate.

// indexManifest builds a one-type manifest carrying the given entries.
func indexManifest(entries []Index) Manifest {
	return Manifest{
		SchemaVersion: SupportedSchemaVersion,
		Name:          "ix",
		Types:         []Type{{Name: "note", Scope: []string{"Notes/**"}}},
		Indexes:       entries,
	}
}

func TestLoadValidatesIndexEntries(t *testing.T) {
	valid := Index{File: "index.md", Lists: "Resources/**/*.md",
		Policy: "must-link-all", Severity: "warning"}

	for label, tc := range map[string]struct {
		entries []Index
		wantErr string // "" = must load
	}{
		"valid must-link-all": {
			entries: []Index{valid},
		},
		"valid must-link-all-and-resolve, error severity": {
			entries: []Index{{File: "Recipes.md", Lists: "Recipes/*.md",
				Policy: "must-link-all-and-resolve", Severity: "error"}},
		},
		"no entries at all": {
			entries: nil,
		},
		"missing file": {
			entries: []Index{{Lists: "Resources/**", Policy: "must-link-all", Severity: "warning"}},
			wantErr: "file is required",
		},
		"missing lists": {
			entries: []Index{{File: "index.md", Policy: "must-link-all", Severity: "warning"}},
			wantErr: "lists is required",
		},
		"malformed lists glob": {
			entries: []Index{{File: "index.md", Lists: "[unclosed",
				Policy: "must-link-all", Severity: "warning"}},
			wantErr: "[unclosed",
		},
		"unknown policy": {
			entries: []Index{{File: "index.md", Lists: "Resources/**",
				Policy: "link-most", Severity: "warning"}},
			wantErr: `"link-most"`,
		},
		"missing severity": {
			entries: []Index{{File: "index.md", Lists: "Resources/**", Policy: "must-link-all"}},
			wantErr: "requires severity",
		},
		"unknown severity": {
			entries: []Index{{File: "index.md", Lists: "Resources/**",
				Policy: "must-link-all", Severity: "loud"}},
			wantErr: "requires severity",
		},
		"duplicate file": {
			entries: []Index{valid, {File: "index.md", Lists: "Projects/**",
				Policy: "must-link-all", Severity: "error"}},
			wantErr: "duplicate",
		},
	} {
		t.Run(label, func(t *testing.T) {
			p, err := loadManifest(t, indexManifest(tc.entries))
			if tc.wantErr == "" {
				require.NoError(t, err)
				require.Len(t, p.Indexes, len(tc.entries))
				return
			}
			require.Error(t, err, "the entry must be rejected at load")
			require.ErrorContains(t, err, tc.wantErr)
			require.ErrorContains(t, err, "indexes", "the error must name the table")
		})
	}
}

// The error must locate the offending entry: by file when it has one, by
// position when it does not.
func TestIndexEntryErrorsNameTheEntry(t *testing.T) {
	_, err := loadManifest(t, indexManifest([]Index{
		{File: "Projects.md", Lists: "Projects/**", Policy: "must-link-all", Severity: "warning"},
		{File: "Recipes.md", Lists: "Recipes/*.md", Policy: "must-link-all"},
	}))
	require.Error(t, err)
	require.ErrorContains(t, err, "Recipes.md", "the error must name the offending entry's file")
}

// The shipped profiles must load through the new gate, and every declared
// entry must carry the fields the coming enforcement swap will read.
func TestBuiltinIndexDeclarationsValidate(t *testing.T) {
	for _, name := range Builtins() {
		t.Run(name, func(t *testing.T) {
			p, err := Load(name)
			require.NoError(t, err)
			for _, ix := range p.Indexes {
				require.NotEmpty(t, ix.Severity, "%s: %s has no severity", name, ix.File)
			}
		})
	}

	// The rdegges declarations mirror the shipped index rules exactly:
	// Projects.md and index.md are warnings, the recipes index is an error
	// and additionally requires its links to resolve.
	p, err := Load("rdegges")
	require.NoError(t, err)
	bySev := map[string]string{}
	byPolicy := map[string]string{}
	for _, ix := range p.Indexes {
		bySev[ix.File] = ix.Severity
		byPolicy[ix.File] = ix.Policy
	}
	require.Equal(t, "warning", bySev["Projects.md"])
	require.Equal(t, "warning", bySev["index.md"])
	require.Equal(t, "error", bySev["Resources/Personal/Recipes/Recipes.md"])
	require.Equal(t, "must-link-all-and-resolve",
		byPolicy["Resources/Personal/Recipes/Recipes.md"],
		"the recipes contract carries the reverse-resolution semantics")
}

// Wrong-typed field values are rejected before this gate ever runs: Index's
// fields are string-typed, so TOML decode itself fails. Pinned as existing
// behavior so the gate's red-first log stays honest — these cannot be
// "observed failing" against pre-gate code.
func TestWrongTypedIndexFieldsFailAtDecode(t *testing.T) {
	dir := t.TempDir()
	manifest := "schema_version = 1\nname = \"ix\"\nscaffold = [\"Notes\"]\n\n" +
		"[[types]]\nname = \"note\"\nscope = [\"Notes/**\"]\n\n" +
		"[[indexes]]\nfile = 42\nlists = \"Resources/**\"\npolicy = \"must-link-all\"\nseverity = \"warning\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.toml"), []byte(manifest), 0o644))
	_, err := Load(dir)
	require.Error(t, err)
}
