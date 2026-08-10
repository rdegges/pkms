package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// connectMCP builds the real server and connects an in-memory client to it,
// so the tools run against whatever vault the test env configures.
func connectMCP(t *testing.T) (*mcp.ClientSession, context.Context) {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	srv := newMCPServer()
	go func() { _ = srv.Run(ctx, st) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs, ctx
}

func callText(t *testing.T, cs *mcp.ClientSession, ctx context.Context, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err, "transport-level call must succeed")
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok, "tool returns text content")
	return tc.Text, res.IsError
}

func TestMCPExposesOnlyReadTools(t *testing.T) {
	cs, ctx := connectMCP(t)
	res, err := cs.ListTools(ctx, nil)
	require.NoError(t, err)
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	require.Equal(t, map[string]bool{"query": true, "lint": true, "profile_show": true}, got,
		"MCP exposes exactly the three read tools — no write surface (§32.6)")
}

// The load-bearing §32.6 guarantee: each MCP tool's output is BYTE-IDENTICAL
// to the corresponding CLI --json path, because both marshal through the
// same producer (jsonapi.go). If they ever diverge, this fails.
func TestMCPProfileShowMatchesCLI(t *testing.T) {
	testEnv(t)
	vaultPath := filepath.Join(t.TempDir(), "v")
	_, err := runCLI(t, "init", "--path", vaultPath)
	require.NoError(t, err)

	cs, ctx := connectMCP(t)
	got, isErr := callText(t, cs, ctx, "profile_show", map[string]any{"vault": "v"})
	require.False(t, isErr, got)

	cliOut, err := runCLI(t, "profile", "show", "--vault", "v", "--json")
	require.NoError(t, err)
	require.Equal(t, cliOut, got, "MCP profile_show must equal `pkms profile show --json` byte-for-byte")
}

func TestMCPLintMatchesCLI(t *testing.T) {
	testEnv(t)
	vaultPath := filepath.Join(t.TempDir(), "v")
	_, err := runCLI(t, "init", "--path", vaultPath)
	require.NoError(t, err)

	cs, ctx := connectMCP(t)
	got, isErr := callText(t, cs, ctx, "lint", map[string]any{"vault": "v"})
	require.False(t, isErr, got)

	// The CLI lint may exit 1 on findings (errFindings); that's not a
	// serialization failure — capture its stdout regardless.
	cliOut, _ := runCLI(t, "lint", "--vault", "v", "--json")
	require.Equal(t, cliOut, got, "MCP lint must equal `pkms lint --json` byte-for-byte")
}

func TestMCPQueryMatchesCLI(t *testing.T) {
	testEnv(t)
	vaultPath := filepath.Join(t.TempDir(), "v")
	_, err := runCLI(t, "init", "--path", vaultPath)
	require.NoError(t, err)

	cs, ctx := connectMCP(t)
	got, isErr := callText(t, cs, ctx, "query", map[string]any{"vault": "v", "type": "clip"})
	require.False(t, isErr, got)

	cliOut, err := runCLI(t, "query", "--vault", "v", "--type", "clip", "--json")
	require.NoError(t, err)
	require.Equal(t, cliOut, got, "MCP query must equal `pkms query --json` byte-for-byte")
}

// A read tool over a bad request reports a tool error, never a transport
// crash — and never mutates anything (there is nothing to mutate).
func TestMCPUnknownVaultIsToolError(t *testing.T) {
	testEnv(t)
	cs, ctx := connectMCP(t)
	got, isErr := callText(t, cs, ctx, "profile_show", map[string]any{"vault": "ghost"})
	require.True(t, isErr, "an unknown vault is a tool error")
	require.NotEmpty(t, got)
}
