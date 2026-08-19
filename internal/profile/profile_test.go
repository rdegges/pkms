package profile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadBuiltins(t *testing.T) {
	for _, name := range Builtins() {
		p, err := Load(name)
		require.NoError(t, err, name)
		require.Equal(t, name, p.Name)
		require.NotEmpty(t, p.Scaffold)
	}
}

func TestLoadUnknown(t *testing.T) {
	_, err := Load("zettelkasten")
	require.ErrorContains(t, err, "not a built-in")
}

// A malformed scope glob must fail profile load, not silently classify
// nothing: TypeOf drives placement and lint severity, so a broken scope
// would misfile every note of that type without a word (issue #30).
func TestLoadRejectsMalformedScopeGlob(t *testing.T) {
	dir := t.TempDir()
	manifest := `schema_version = 1
name = "broken"
scaffold = ["Notes"]

[[types]]
name = "note"
scope = ["Notes/**", "[unclosed"]
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.toml"), []byte(manifest), 0o644))
	_, err := Load(dir)
	require.Error(t, err, "a malformed type scope glob must fail load")
	require.ErrorContains(t, err, "[unclosed", "the error must name the offending pattern")
	require.ErrorContains(t, err, "note", "the error must name the type")

	// The same manifest with the bad pattern removed loads fine — proving
	// the rejection above is the glob check, not some other decode error.
	good := `schema_version = 1
name = "ok"
scaffold = ["Notes"]

[[types]]
name = "note"
scope = ["Notes/**"]
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.toml"), []byte(good), 0o644))
	_, err = Load(dir)
	require.NoError(t, err)
}

func TestTypeOfClassification(t *testing.T) {
	p, err := Load("rdegges")
	require.NoError(t, err)

	cases := []struct {
		path   string
		fields map[string]any
		want   string
	}{
		{"People/Snyk/Jane Doe.md", nil, "person"},
		{"Meetings/Snyk/2026/05/06/1100 - Weekly Sync.md", nil, "meeting"},
		{"Meetings/Snyk/2026/05/06/daily-brief.md", nil, "daily-brief"},
		{"Meetings/Snyk/2026/05/06/pre-brief.md", nil, "pre-brief"},
		{"Projects/Personal/side-project.md", nil, "project"},
		{"Resources/Personal/Recipes/Recipes.md", nil, "recipe-index"},
		{"Resources/Personal/Recipes/Beef Stew.md", nil, "recipe"},
		{"Resources/Personal/Session Traces/2026-07-03 — setup.md", nil, "session-trace"},
		{"Resources/Clips/Inbox/clip.md", nil, "raw-clip"},
		{"Resources/Snyk/Some Article.md", map[string]any{"source_url": "https://x"}, "clip-summary"},
		{"Resources/Snyk/Some Synthesis.md", map[string]any{"type": "resource"}, "resource-generic"},
		{"Areas/Writing/Draft.md", nil, "area"},
		{"Archive/Snyk/Old.md", nil, "archived"},
		{"Action Items.md", nil, "action-items"},
		{"Now.md", nil, "canonical-root"},
		{"log.md", nil, "machine-root"},
		{"Random.md", nil, ""},
	}
	for _, c := range cases {
		got, err := p.TypeOf(c.path, c.fields)
		require.NoError(t, err, c.path)
		require.Equal(t, c.want, got, c.path)
	}
}

func TestSchemasValidate(t *testing.T) {
	p, err := Load("rdegges")
	require.NoError(t, err)

	person := p.Schema("person")
	require.NotNil(t, person)
	require.NoError(t, person.Validate(map[string]any{
		"last_met":      "2026-07-15",
		"meeting_count": int64(3),
		"topics":        []any{"ai-security"},
		"extra_key":     "allowed",
	}))
	require.Error(t, person.Validate(map[string]any{
		"last_met": nil, "meeting_count": int64(3), "topics": []any{},
	}), "null last_met fails")

	recipe := p.Schema("recipe")
	require.Error(t, recipe.Validate(map[string]any{
		"type": "recipe", "servings": int64(4), "course": "main",
		"tags": []any{"recipe"}, "calories_per_serving": int64(450),
	}), "calories without macros_estimated fails (dependentRequired)")
	require.NoError(t, recipe.Validate(map[string]any{
		"type": "recipe", "servings": int64(4), "course": "main",
		"tags": []any{"recipe"}, "calories_per_serving": int64(450),
		"macros_estimated": true,
	}))
}

func TestRenderPath(t *testing.T) {
	p, err := Load("rdegges")
	require.NoError(t, err)

	folder, filename, err := p.RenderPath("meeting", map[string]any{
		"category": "Snyk", "date": "2026-05-06", "hhmm": "1100", "title": "Weekly Sync",
	})
	require.NoError(t, err)
	require.Equal(t, "Meetings/Snyk/2026/05/06", folder)
	require.Equal(t, "1100 - Weekly Sync", filename)

	_, _, err = p.RenderPath("meeting", map[string]any{
		"category": "../../etc", "date": "2026-05-06", "hhmm": "1100", "title": "x",
	})
	require.ErrorContains(t, err, "escapes the vault")

	_, _, err = p.RenderPath("meeting", map[string]any{"category": "Snyk"})
	require.Error(t, err, "missing fields are errors, not empty strings")
}

func TestEjectRoundTrips(t *testing.T) {
	p, err := Load("para")
	require.NoError(t, err)

	dest := filepath.Join(t.TempDir(), "para")
	require.NoError(t, p.Eject(dest))
	_, err = os.Stat(filepath.Join(dest, "profile.toml"))
	require.NoError(t, err)

	p2, err := Load(dest)
	require.NoError(t, err)
	require.Equal(t, "para", p2.Name)
}
