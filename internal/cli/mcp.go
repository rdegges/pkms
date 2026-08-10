package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/query"
	"github.com/rdegges/pkms/internal/vault"
)

// `pkms mcp` is a read-only stdio MCP server for non-Claude hosts (SPEC
// §32.6). Its tools call the SAME JSON producers as the CLI --json paths
// (jsonapi.go) — one serializer, no drift. It exposes NO write tools: a
// host reaching pkms over MCP has none of the plugin's safety protocol, so
// ingest/lint --fix/filing stay off MCP.

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run a read-only MCP server over stdio (query, lint, profile) for non-Claude hosts",
		Long: `Serve pkms's read surfaces to any Model Context Protocol host over
stdio. Exposes query, lint, and profile_show as MCP tools — the same
deterministic JSON the CLI's --json flags produce. Read-only: it never
writes to a vault. Inside Claude Code, use the plugin's CLI skill instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMCP(cmd)
		},
	}
}

func runMCP(cmd *cobra.Command) error {
	err := newMCPServer().Run(cmd.Context(), &mcp.StdioTransport{})
	// A closed stdin ends the session normally (the jsonrpc2 layer reports
	// it as an EOF-bearing "server is closing" error that does not unwrap
	// to io.EOF). That is not a failure — exit 0. Surface anything else.
	if err == nil || errors.Is(err, io.EOF) || strings.Contains(err.Error(), "EOF") {
		return nil
	}
	return err
}

// newMCPServer builds the read-only server and registers its tools. Split
// from runMCP so tests can drive it over an in-memory transport.
func newMCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:        "pkms",
		Version:     version,
		Description: "Read-only access to a pkms vault: query notes, lint, and inspect the profile.",
	}, nil)

	// vaultTarget is the shared input: an optional vault name, resolved the
	// same way the CLI resolves --vault (single vault → implicit).
	type vaultTarget struct {
		Vault string `json:"vault,omitempty" jsonschema:"the configured vault name; omit when only one vault is configured"`
	}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "profile_show",
		Description: "Show a vault's profile: scaffold folders, note types (in classification order) with inlined JSON Schemas, and the ingest/capture mapping. Read structure here instead of assuming folder names.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in vaultTarget) (*mcp.CallToolResult, any, error) {
		prof, err := mcpProfile(in.Vault)
		if err != nil {
			return toolError(err), nil, nil
		}
		b, err := profileJSON(prof)
		if err != nil {
			return toolError(err), nil, nil
		}
		return toolText(b), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "lint",
		Description: "Run the vault's deterministic lint rules and return findings (rule, severity, path, line, message, fixable). Read-only — never applies fixes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in vaultTarget) (*mcp.CallToolResult, any, error) {
		v, prof, ix, err := mcpVault(in.Vault)
		if err != nil {
			return toolError(err), nil, nil
		}
		b, err := lintJSON(v.Name, ix, prof, v.Lint, nil)
		if err != nil {
			return toolError(err), nil, nil
		}
		return toolText(b), nil, nil
	})

	type queryInput struct {
		Vault     string   `json:"vault,omitempty" jsonschema:"the configured vault name; omit when only one vault is configured"`
		Type      string   `json:"type,omitempty" jsonschema:"profile note type to filter by"`
		Where     []string `json:"where,omitempty" jsonschema:"frontmatter filters as key=value (AND-combined)"`
		Text      string   `json:"text,omitempty" jsonschema:"case-insensitive substring over note bodies"`
		Backlinks string   `json:"backlinks,omitempty" jsonschema:"vault-relative path; return notes linking to it"`
		Orphans   bool     `json:"orphans,omitempty" jsonschema:"return only notes with no inbound links"`
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "query",
		Description: "Deterministic retrieval over a vault: field filters, full-text, and backlinks. Returns each note's path and frontmatter. Cite only paths this returns.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, any, error) {
		_, prof, ix, err := mcpVault(in.Vault)
		if err != nil {
			return toolError(err), nil, nil
		}
		where := map[string]string{}
		for _, w := range in.Where {
			k, val, ok := splitKV(w)
			if !ok {
				return toolError(fmt.Errorf("where wants key=value, got %q", w)), nil, nil
			}
			where[k] = val
		}
		b, err := queryJSON(ix, prof, query.Options{
			Type: in.Type, Where: where, Text: in.Text,
			Backlinks: in.Backlinks, Orphans: in.Orphans,
		})
		if err != nil {
			return toolError(err), nil, nil
		}
		return toolText(b), nil, nil
	})

	return srv
}

func toolText(b []byte) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

// toolError returns a tool-call result flagged as an error (the MCP way to
// report a failed call — the host sees IsError, not a transport fault).
func toolError(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}

func splitKV(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			if i == 0 {
				return "", "", false
			}
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

// mcpVault resolves a vault by name (or the single configured vault) and
// builds its profile + index — the read fixture query/lint need.
func mcpVault(name string) (*config.Vault, *profile.Profile, *vault.Index, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, nil, nil, err
	}
	v, err := cfg.Vault(name)
	if err != nil {
		return nil, nil, nil, err
	}
	prof, err := profile.Load(v.Profile)
	if err != nil {
		return nil, nil, nil, err
	}
	ix, err := vault.BuildIndex(v.Path, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	if err != nil {
		return nil, nil, nil, err
	}
	return v, prof, ix, nil
}

// mcpProfile resolves just the profile: a vault name shows that vault's
// profile; empty resolves the single configured vault (profile_show over
// MCP is always vault-scoped — hosts don't pass profile paths).
func mcpProfile(name string) (*profile.Profile, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, err
	}
	v, err := cfg.Vault(name)
	if err != nil {
		return nil, err
	}
	return profile.Load(v.Profile)
}
