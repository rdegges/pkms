package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file hardens the §32.5 prompt gate defined in promptrot_test.go.
// It (1) closes integrity gaps that would let the gate pass vacuously, and
// (2) characterizes known fail-open extraction gaps so any change in the
// extractor's behavior surfaces loudly instead of silently. The KNOWN_GAP
// tests assert CURRENT (holey) behavior on purpose — see each test's note
// for the recommended hardening. When the extractor is fixed, these flip red
// and must be updated to assert the hardened behavior.

// --- Gate-integrity: the checks must not be able to pass as no-ops. ---

// The folder-literal ban is only meaningful if bannedFolders actually yields
// folders. If profile loading ever returned nothing, the ban would silently
// become a no-op and TestShippedPrompts...FolderLiterals would still pass.
// Pin the invariant: the derived set is non-empty and includes the generic
// profile's capture folder.
func TestGateBannedFoldersNonEmpty(t *testing.T) {
	got := bannedFolders(t)
	require.NotEmpty(t, got, "banned-folder set is empty — the folder ban is a no-op")
	require.Contains(t, got, "_Inbox", "expected the para profile capture folder in the banned set")
}

// TestReadmeCommandsResolve passes vacuously if the README carries no pkms
// invocations at all. Pin that the README actually exercises the resolver.
func TestReadmeContainsResolvableInvocations(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "README.md"))
	require.NoError(t, err)
	require.NotEmpty(t, invocations(string(raw)),
		"README carries no code-span pkms invocations — TestReadmeCommandsResolve is vacuous")
}

// The shipped-prompt sweep must actually discover the two skills, not just
// "some non-empty set". If a skill moves or its front matter breaks the
// glob, NotEmpty alone would not notice.
func TestShippedPromptsDiscoverBothSkills(t *testing.T) {
	files := shippedPromptFiles(t)
	root := repoRoot(t)
	var rels []string
	for _, f := range files {
		r, _ := filepath.Rel(root, f)
		rels = append(rels, r)
	}
	require.Contains(t, rels, filepath.Join("skills", "cli", "SKILL.md"))
	require.Contains(t, rels, filepath.Join("skills", "process-inbox", "SKILL.md"))
}

// --- KNOWN_GAP characterization: fail-open extraction paths. ---
// Each builds a prompt that satisfies the floor with one VALID command and
// hides one BAD command via a pattern the extractor mishandles. A hardened
// gate would return a violation; today it returns none. These pin the gap.

// GAP 1 — compound line: everything after `&&` is swallowed as positional
// args to the first command and never resolved. `pkms frobnicate` escapes.
// RECOMMENDED FIX: split each extracted line on shell separators (&&, ||, ;,
// |) into separate invocations before resolving; or reject lines containing
// a bare separator as un-analyzable.
func TestGateCompoundCommandSecondIsValidated(t *testing.T) {
	md := "```\npkms snapshot\npkms lint --json && pkms frobnicate\n```\n"
	v := checkPrompt(t, "gap", md, true, true)
	require.NotEmpty(t, v, "a bad command after && must be caught")
	require.Contains(t, strings.Join(v, "\n"), "not a pkms command")
}

// GAP 2 — a backslash-continued line immediately before the closing fence is
// silently dropped (codePieces resets `pending` on the fence boundary without
// flushing it). The floor is met by the valid command, so the dropped bad
// command escapes entirely.
// RECOMMENDED FIX: flush any non-empty `pending` as a completed line when a
// fence closes (and at end of input) instead of discarding it.
func TestGateBackslashBeforeFenceCloseIsFlushed(t *testing.T) {
	md := "```\npkms snapshot\npkms frobnicate --json \\\n```\n"
	v := checkPrompt(t, "gap", md, true, true)
	require.NotEmpty(t, v, "a backslash-continued command at fence close must not be dropped")
	require.Contains(t, strings.Join(v, "\n"), "not a pkms command")
}

// GAP 3 — a command not at the start of a fenced line (e.g. a `$ ` shell
// prompt prefix) is not recognized as an invocation and is never validated.
// RECOMMENDED FIX: strip a leading shell-prompt sigil, or scan for `pkms `
// anywhere on the line rather than only at index 0.
func TestGateShellPromptPrefixIsValidated(t *testing.T) {
	md := "```\npkms snapshot\n$ pkms frobnicate --json\n```\n"
	v := checkPrompt(t, "gap", md, true, true)
	require.NotEmpty(t, v, "a `$ `-prefixed bad command must be caught")
	require.Contains(t, strings.Join(v, "\n"), "not a pkms command")
}

// An env-var-prefixed bad command is also caught.
func TestGateEnvPrefixIsValidated(t *testing.T) {
	md := "```\npkms snapshot\nVAULT=x pkms frobnicate\n```\n"
	v := checkPrompt(t, "gap", md, true, true)
	require.NotEmpty(t, v, "an env-prefixed bad command must be caught")
	require.Contains(t, strings.Join(v, "\n"), "not a pkms command")
}

// GAP 4 — the folder ban is a naive substring match, so a banned scaffold
// word (e.g. "Areas") trips inside an unrelated token like "AreasOfLaw".
// This is over-blocking (fail-closed, so safe) but can force awkward prompt
// wording. RECOMMENDED FIX: match folder literals on path/word boundaries
// (e.g. as a whole path segment) rather than by raw substring.
func TestGateFolderBanMatchesOnBoundaries(t *testing.T) {
	// "AreasOfLaw" is not the folder "Areas" — a boundary-aware ban must not
	// false-positive on it...
	ok := "```\npkms query --where topic=AreasOfLaw --json\n```\n"
	require.Empty(t, checkPrompt(t, "gap", ok, true, true),
		"a banned word inside a larger token must not trip the ban")

	// ...but a real capture-folder literal used as a path still trips it.
	bad := "```\npkms query --type clip --json\n```\n\nNotes live in `_Inbox/`.\n"
	require.Contains(t, strings.Join(checkPrompt(t, "gap", bad, true, true), "\n"), "folder literal")
}
