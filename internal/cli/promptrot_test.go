package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/profile"
)

// The prompt gate (SPEC §32.5): a shipped prompt (or the README) may not
// tell an agent to run a pkms command that does not exist, and a shipped
// prompt may not hard-code a profile's folder names (that would defeat the
// vault-agnosticism `profile show` exists to provide). This test extracts
// every `pkms …` invocation written in a code span/fence and resolves it
// against the REAL cobra tree, enforces a constant floor, and bans
// folder literals derived AT RUNTIME from the embedded profiles.

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	// this file is internal/cli/promptrot_test.go → root is ../..
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// tokenize splits a command line on whitespace, respecting single and
// double quotes (placeholders like <url> and _paths_ pass through as-is).
func tokenize(s string) []string {
	var toks []string
	var b strings.Builder
	var quote rune
	flush := func() {
		if b.Len() > 0 {
			toks = append(toks, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t':
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return toks
}

func findSub(c *cobra.Command, name string) *cobra.Command {
	for _, sub := range c.Commands() {
		if sub.Name() == name {
			return sub
		}
		for _, a := range sub.Aliases {
			if a == name {
				return sub
			}
		}
	}
	return nil
}

// resolveInvocation returns an error if cmdline names a command or flag the
// real binary does not have. It descends subcommands by leading non-flag
// tokens (an unknown token where a subcommand is required is rejected —
// this is what catches `pkms frobnicate`), then validates the remaining
// flags against that command's full flag set.
func resolveInvocation(cmdline string) error {
	toks := tokenize(cmdline)
	if len(toks) == 0 || toks[0] != "pkms" {
		return fmt.Errorf("not a pkms invocation: %q", cmdline)
	}
	cur := newRootCmd()
	args := toks[1:]
	i := 0
	for i < len(args) {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			break // flags begin; stop descending
		}
		if sub := findSub(cur, a); sub != nil {
			cur = sub
			i++
			continue
		}
		// Not a subcommand. If cur is a pure group (not runnable but has
		// subcommands), an unknown leading token is an unknown subcommand.
		if !cur.Runnable() && cur.HasSubCommands() {
			return fmt.Errorf("%q: %q is not a pkms command", cmdline, a)
		}
		break // a positional argument for a runnable command
	}
	if err := cur.ParseFlags(args[i:]); err != nil {
		return fmt.Errorf("%q: %w", cmdline, err)
	}
	return nil
}

// codePieces returns the text of every fenced block and inline code span in
// a markdown document. Fenced-block lines ending in `\` are joined so a
// line-wrapped command is seen whole (and validated, never skipped).
func codePieces(md string) (fencedLines, inlineSpans []string) {
	lines := strings.Split(md, "\n")
	inFence := false
	var pending string
	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "```") {
			inFence = !inFence
			pending = ""
			continue
		}
		if inFence {
			joined := pending + ln
			if strings.HasSuffix(strings.TrimRight(joined, " \t"), "\\") {
				pending = strings.TrimRight(strings.TrimRight(joined, " \t"), "\\") + " "
				continue
			}
			pending = ""
			fencedLines = append(fencedLines, joined)
		}
	}
	// Inline spans on non-fenced lines: `...` (single backtick, single line).
	inFence = false
	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for {
			i := strings.IndexByte(ln, '`')
			if i < 0 {
				break
			}
			rest := ln[i+1:]
			j := strings.IndexByte(rest, '`')
			if j < 0 {
				break
			}
			inlineSpans = append(inlineSpans, rest[:j])
			ln = rest[j+1:]
		}
	}
	return fencedLines, inlineSpans
}

// invocations returns every `pkms …` command written in a document's code.
func invocations(md string) []string {
	fenced, inline := codePieces(md)
	var out []string
	for _, ln := range fenced {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue // blank or comment line
		}
		if idx := strings.Index(t, "pkms "); idx == 0 || t == "pkms" {
			out = append(out, t)
		}
	}
	for _, sp := range inline {
		t := strings.TrimSpace(sp)
		if t == "pkms" || strings.HasPrefix(t, "pkms ") {
			out = append(out, t)
		}
	}
	return out
}

// bannedFolders derives, at test runtime, the folder literals a shipped
// prompt must not hard-code: every embedded profile's scaffold entries plus
// its ingest capture folders (the clip/asset type folders). Templated
// folders (`{{…}}`) are not literals and are skipped.
func bannedFolders(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !strings.Contains(s, "{{") {
			seen[s] = true
		}
	}
	for _, name := range profile.Builtins() {
		p, err := profile.Load(name)
		require.NoError(t, err)
		for _, s := range p.Scaffold {
			add(s)
		}
		for _, typeName := range []string{p.Ingest.Clip, p.Ingest.Asset} {
			if ty := p.Type(typeName); ty != nil {
				add(ty.Folder)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	// Longest first so a nested path is reported before its prefix.
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// checkPrompt runs the gate over one document. floor requires ≥1 resolvable
// invocation (shipped prompts; false for the README). banFolders enables the
// folder-literal ban (shipped prompts only). Returns violation messages.
func checkPrompt(t *testing.T, name, md string, floor, banFolders bool) []string {
	t.Helper()
	var v []string
	invs := invocations(md)
	for _, inv := range invs {
		if err := resolveInvocation(inv); err != nil {
			v = append(v, fmt.Sprintf("%s: unresolvable command: %v", name, err))
		}
	}
	if floor && len(invs) == 0 {
		v = append(v, fmt.Sprintf("%s: shipped prompt carries no resolvable pkms invocation", name))
	}
	if banFolders {
		fenced, inline := codePieces(md)
		code := strings.Join(append(fenced, inline...), "\n")
		for _, folder := range bannedFolders(t) {
			if strings.Contains(code, folder) {
				v = append(v, fmt.Sprintf("%s: hard-codes profile folder literal %q (read it from `pkms profile show` instead)", name, folder))
			}
		}
	}
	return v
}

// shippedPromptFiles returns the agent and skill markdown that ships in the
// plugin — the prompts the gate polices with a floor and the folder ban.
func shippedPromptFiles(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	var files []string
	for _, glob := range []string{"agents/*.md", "skills/*/SKILL.md"} {
		matches, err := filepath.Glob(filepath.Join(root, glob))
		require.NoError(t, err)
		files = append(files, matches...)
	}
	return files
}

func TestShippedPromptsResolveAndAvoidFolderLiterals(t *testing.T) {
	files := shippedPromptFiles(t)
	require.NotEmpty(t, files, "no shipped prompts found — did the plugin move?")
	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		rel, _ := filepath.Rel(repoRoot(t), f)
		v := checkPrompt(t, rel, string(raw), true, true)
		require.Empty(t, v, "%s", strings.Join(v, "\n"))
	}
}

func TestReadmeCommandsResolve(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "README.md"))
	require.NoError(t, err)
	// README: resolve-checked, no floor, no folder ban (§32.5).
	v := checkPrompt(t, "README.md", string(raw), false, false)
	require.Empty(t, v, "%s", strings.Join(v, "\n"))
}

// --- gate discipline (§15/§32.5): the gate must be observed rejecting each
// seeded violation before it counts as installed. ---

func TestGateRejectsFakeSubcommand(t *testing.T) {
	md := "```\npkms frobnicate --json\n```\n"
	v := checkPrompt(t, "seed", md, true, true)
	require.NotEmpty(t, v, "a fake subcommand must be rejected")
	require.Contains(t, strings.Join(v, "\n"), "not a pkms command")
}

func TestGateRejectsLineWrappedBadInvocation(t *testing.T) {
	// A command wrapped across lines with a trailing backslash: the gate
	// must JOIN it and still catch the unknown flag — never skip the
	// incomplete first line as unparseable.
	md := "```\npkms query --type clip \\\n  --nonexistent-flag\n```\n"
	v := checkPrompt(t, "seed", md, true, true)
	require.NotEmpty(t, v, "a line-wrapped bad invocation must be rejected")
	require.Contains(t, strings.Join(v, "\n"), "nonexistent-flag")
}

func TestGateRejectsFolderLiteral(t *testing.T) {
	// A valid command, but the prompt hard-codes a capture folder literal.
	md := "Do this:\n\n```\npkms query --type clip --json\n```\n\nNotes live in `_Inbox/`.\n"
	v := checkPrompt(t, "seed", md, true, true)
	require.NotEmpty(t, v, "a hard-coded folder literal must be rejected")
	require.Contains(t, strings.Join(v, "\n"), "folder literal")
}

// The floor: a shipped prompt with no resolvable invocation is a violation.
func TestGateEnforcesInvocationFloor(t *testing.T) {
	v := checkPrompt(t, "seed", "# A prompt with no commands at all\n", true, true)
	require.NotEmpty(t, v)
	require.Contains(t, strings.Join(v, "\n"), "no resolvable pkms invocation")
}

// A clean, valid prompt passes — proving the gate is not merely always-red.
func TestGateAcceptsValidPrompt(t *testing.T) {
	md := "Run:\n\n```\npkms profile show --vault <name> --json\npkms query --type clip --json\npkms snapshot\n```\n"
	v := checkPrompt(t, "seed", md, true, true)
	require.Empty(t, v, "%s", strings.Join(v, "\n"))
}
