package cli

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// setupMCPVault registers a populated rdegges vault ("lintv") whose notes make
// every query predicate discriminate: a person that links another (so the
// linked one is not an orphan and is a backlink target), a differently-scoped
// person, and a non-person note. Each filter below therefore returns a strict,
// non-empty subset — so a mismapped queryInput field would change the CLI-vs-MCP
// bytes and fail the comparison, not hide behind an empty result.
func setupMCPVault(t *testing.T) {
	t.Helper()
	setupLintVault(t, map[string]string{
		"People/Snyk/Alice.md":   "---\ncategory: Snyk\n---\nzebra unique-alice\n[[Bob]]\n",
		"People/Snyk/Bob.md":     "---\ncategory: Personal\n---\nplain bob\n",
		"Areas/Personal/misc.md": "plain misc, no frontmatter\n",
	})
}

// assertMCPQueryMatchesCLI proves the query tool passes its typed input through
// to the same producer the CLI --json path uses: same options in, byte-identical
// bytes out. cliArgs are the flags equivalent to mcpArgs.
func assertMCPQueryMatchesCLI(t *testing.T, cs *mcp.ClientSession, ctx context.Context, mcpArgs map[string]any, cliArgs ...string) {
	t.Helper()
	got, isErr := callText(t, cs, ctx, "query", mcpArgs)
	require.False(t, isErr, got)
	require.NotEqual(t, "{\n  \"results\": []\n}\n", got,
		"fixture must make this predicate return a non-empty subset, else a dropped filter would go unnoticed")

	cliOut, err := runCLI(t, append([]string{"query", "--vault", "lintv", "--json"}, cliArgs...)...)
	require.NoError(t, err)
	require.Equal(t, cliOut, got)
}

// Each query option must reach query.Options unchanged. type=clip (empty) in the
// spec-owned mcp_test.go can't catch a field swap (e.g. Backlinks<-Text); these
// discriminating fixtures can.
func TestMCPQueryOptionsMapToCLI(t *testing.T) {
	setupMCPVault(t)
	cs, ctx := connectMCP(t)

	t.Run("type", func(t *testing.T) {
		assertMCPQueryMatchesCLI(t, cs, ctx,
			map[string]any{"vault": "lintv", "type": "person"},
			"--type", "person")
	})
	t.Run("where", func(t *testing.T) {
		assertMCPQueryMatchesCLI(t, cs, ctx,
			map[string]any{"vault": "lintv", "where": []string{"category=Snyk"}},
			"--where", "category=Snyk")
	})
	t.Run("text", func(t *testing.T) {
		assertMCPQueryMatchesCLI(t, cs, ctx,
			map[string]any{"vault": "lintv", "text": "zebra"},
			"--text", "zebra")
	})
	t.Run("orphans", func(t *testing.T) {
		assertMCPQueryMatchesCLI(t, cs, ctx,
			map[string]any{"vault": "lintv", "orphans": true},
			"--orphans")
	})
	t.Run("backlinks", func(t *testing.T) {
		assertMCPQueryMatchesCLI(t, cs, ctx,
			map[string]any{"vault": "lintv", "backlinks": "People/Snyk/Bob.md"},
			"--backlinks", "People/Snyk/Bob.md")
	})
}

// A malformed where entry is a tool error, not a transport crash — and the KV
// parse rejects the same shapes the CLI's strings.Cut path rejects.
func TestMCPQueryBadWhereIsToolError(t *testing.T) {
	setupMCPVault(t)
	cs, ctx := connectMCP(t)

	for _, bad := range []string{"novalue", "=orphanvalue"} {
		got, isErr := callText(t, cs, ctx, "query",
			map[string]any{"vault": "lintv", "where": []string{bad}})
		require.True(t, isErr, "malformed where %q must be a tool error", bad)
		require.Contains(t, got, "key=value")
	}
}

// The spec-owned lint byte-identity test runs against a clean vault (empty
// findings). This pins the non-empty path: real findings, and the summary
// counts, marshal identically through the shared producer.
func TestMCPLintWithFindingsMatchesCLI(t *testing.T) {
	setupLintVault(t, map[string]string{
		"People/Snyk/Broken.md": "# no frontmatter\n",
	})
	cs, ctx := connectMCP(t)

	got, isErr := callText(t, cs, ctx, "lint", map[string]any{"vault": "lintv"})
	require.False(t, isErr, got)
	require.Contains(t, got, "\"findings\": [", "fixture must produce findings")
	require.NotContains(t, got, "\"findings\": []", "findings must be non-empty here")

	// CLI lint exits 1 on findings (errFindings); that is not a serialization
	// failure — capture stdout regardless.
	cliOut, _ := runCLI(t, "lint", "--vault", "lintv", "--json")
	require.Equal(t, cliOut, got, "non-empty lint must equal the CLI byte-for-byte")
}

func TestSplitKV(t *testing.T) {
	cases := []struct {
		in     string
		wantK  string
		wantV  string
		wantOK bool
	}{
		{"a=b", "a", "b", true},
		{"key=value", "key", "value", true},
		{"a=", "a", "", true},       // empty value is allowed
		{"a=b=c", "a", "b=c", true}, // only the first = splits
		{"=b", "", "", false},       // empty key rejected
		{"novalue", "", "", false},  // no separator
		{"", "", "", false},         // empty string
		{"category=Snyk", "category", "Snyk", true},
	}
	for _, c := range cases {
		k, v, ok := splitKV(c.in)
		require.Equal(t, c.wantOK, ok, "ok for %q", c.in)
		require.Equal(t, c.wantK, k, "key for %q", c.in)
		require.Equal(t, c.wantV, v, "value for %q", c.in)
	}
}
