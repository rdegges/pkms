package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The process-inbox skill's canonical §32.4a sequence names a set of pkms
// subcommands. The substrate e2e (28-agent-substrate.txtar) must exercise
// each of them against the real binary — otherwise the skill could tell an
// agent to run a command the substrate never proves works. This ties the
// two together: add a command to the skill's block and the substrate must
// cover it, or this fails (SPEC §32.7 layer 1, "tied to the canonical
// block").
func TestCanonicalBlockCoveredBySubstrate(t *testing.T) {
	root := repoRoot(t)

	skill, err := os.ReadFile(filepath.Join(root, "skills", "process-inbox", "SKILL.md"))
	require.NoError(t, err)
	// The contract is the CANONICAL FENCED BLOCK, not incidental inline
	// mentions in prose (e.g. step 7 explaining what `pkms undo` does) — so
	// read subcommands from fenced code only.
	fenced, _ := codePieces(string(skill))
	subs := map[string]bool{}
	for _, ln := range fenced {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue // blank or comment line, not a command
		}
		for _, seg := range splitShellSegments(ln) {
			if inv := pkmsInvocation(seg); inv != "" {
				if s := leadingSubcommand(inv); s != "" {
					subs[s] = true
				}
			}
		}
	}
	require.NotEmpty(t, subs, "the canonical fenced block names no pkms subcommands — did it move?")

	substrate, err := os.ReadFile(filepath.Join(root, "e2e", "testdata", "28-agent-substrate.txtar"))
	require.NoError(t, err)
	txt := string(substrate)
	for sub := range subs {
		require.Contains(t, txt, "pkms "+sub,
			"canonical block names `pkms %s` but the substrate e2e never exercises it", sub)
	}
}

// leadingSubcommand returns the top-level subcommand of a pkms invocation
// (the first non-flag token after "pkms"), or "" for a bare "pkms".
func leadingSubcommand(inv string) string {
	toks := tokenize(inv)
	for i := 1; i < len(toks); i++ {
		if !strings.HasPrefix(toks[i], "-") {
			return toks[i]
		}
	}
	return ""
}
