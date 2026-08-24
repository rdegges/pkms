package lint

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/profile"
)

// ValidateConfig's green sentence is "every registered rule instantiates".
// With an empty registry that sentence would be vacuously true while
// validating nothing — the exact "nothing to do" green SPEC §15 forbids —
// so it must error instead. In-package: the registry is package state
// populated by the rules package's init, which this test package does not
// import, but it swaps and restores the map anyway so a future blank
// import cannot silently change the premise.
func TestValidateConfigFailsClosedOnAnEmptyRegistry(t *testing.T) {
	saved := registry
	registry = map[string]PeerFactory{}
	t.Cleanup(func() { registry = saved })

	prof, err := profile.Load("para")
	require.NoError(t, err)

	err = ValidateConfig(prof, nil)
	require.Error(t, err, "an empty registry validated nothing and must not report green")
	require.Contains(t, err.Error(), "no lint rules registered")
}
