//go:build e2e

// Package e2e walks the new-user experience end to end: every script under
// testdata/ is one phase of the manual UX checklist, run against pkms as a
// command (argv, exit codes, and output exactly as a user sees them).
//
// Run: go test -tags=e2e ./e2e/...
package e2e

import (
	"os"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/rdegges/pkms/internal/cli"
)

// TestMain re-execs this test binary as the `pkms` command inside scripts,
// through the exact entry point cmd/pkms/main.go uses.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"pkms": func() { os.Exit(cli.Execute()) },
	})
}

func TestNewUserExperience(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata",
		Setup: func(env *testscript.Env) error {
			// Every script gets a hermetic HOME/config/state so nothing
			// touches the real machine.
			env.Setenv("HOME", env.WorkDir+"/home")
			env.Setenv("PKMS_CONFIG", env.WorkDir+"/home/config.toml")
			env.Setenv("XDG_STATE_HOME", env.WorkDir+"/home/state")
			// Never let git prompt or pick up the developer's config.
			env.Setenv("GIT_TERMINAL_PROMPT", "0")
			env.Setenv("GIT_CONFIG_GLOBAL", env.WorkDir+"/home/gitconfig")
			env.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
			return os.MkdirAll(env.WorkDir+"/home", 0o755)
		},
	})
}
