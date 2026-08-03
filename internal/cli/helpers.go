package cli

import (
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/config"
)

func loadConfig(cmd *cobra.Command) (*config.Config, error) {
	return config.Load("") // PKMS_CONFIG / default path
}

// selectedVault resolves --vault against the config.
func selectedVault(cmd *cobra.Command, cfg *config.Config) (*config.Vault, error) {
	name, _ := cmd.Flags().GetString("vault")
	if name == "" {
		name, _ = cmd.Root().PersistentFlags().GetString("vault")
	}
	return cfg.Vault(name)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify derives a vault name from a directory basename.
func slugify(s string) string {
	s = slugRe.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(s, "-")
}
