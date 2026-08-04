package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/ingest/imap"
	"github.com/rdegges/pkms/internal/secrets"
)

func newSecretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Store or remove ingester credentials in the OS keyring",
		Long: `Secrets never live in config.toml. pkms looks them up in this order:
OS keyring → PKMS_<VAULT>_<SOURCE>_<KIND> env var → password_cmd (argv
array, password kind only). This command manages the keyring entries.`,
	}
	cmd.AddCommand(newSecretSetCmd(), newSecretRmCmd())
	return cmd
}

// resolveSourceIngester maps a --source name to its configured entry so
// keyring accounts always carry the <type>:<name> identity.
func resolveSourceIngester(cmd *cobra.Command, args []string) (*config.Vault, config.IngesterConfig, error) {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return nil, config.IngesterConfig{}, err
	}
	v, err := selectedVault(cmd, cfg)
	if err != nil {
		return nil, config.IngesterConfig{}, err
	}
	for _, ic := range v.Sources {
		if ic.Name == args[0] {
			return v, ic, nil
		}
	}
	return nil, config.IngesterConfig{}, fmt.Errorf("vault %q has no ingester named %q (configured: %s); add its [[vaults.ingesters]] entry first",
		v.Name, args[0], sourceNames(v.Sources))
}

func newSecretSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <source> <kind>",
		Short: "Prompt for a secret and store it in the OS keyring",
		Long: `Reads the secret from the terminal (no echo) or from stdin when piped.
Kinds: password, oauth-client-id, oauth-client-secret, oauth-refresh-token.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, ic, err := resolveSourceIngester(cmd, args)
			if err != nil {
				return err
			}
			kind, err := secrets.ParseKind(args[1])
			if err != nil {
				return err
			}
			value, err := readSecret(cmd)
			if err != nil {
				return err
			}
			if value == "" {
				return fmt.Errorf("empty secret; nothing stored")
			}
			if err := secrets.Store(v.Name, ic.Source(), kind, value); err != nil {
				return fmt.Errorf("store in the OS keyring: %w (headless? use the $%s env var or password_cmd instead)",
					err, secrets.EnvVar(v.Name, ic.Name, kind))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "stored %s for %s (keyring account %q)\n",
				kind, ic.Source(), secrets.Account(v.Name, ic.Source(), kind))
			return nil
		},
	}
	return cmd
}

func newSecretRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <source> <kind>",
		Short: "Remove a secret from the OS keyring",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, ic, err := resolveSourceIngester(cmd, args)
			if err != nil {
				return err
			}
			kind, err := secrets.ParseKind(args[1])
			if err != nil {
				return err
			}
			if err := secrets.Delete(v.Name, ic.Source(), kind); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s for %s\n", kind, ic.Source())
			return nil
		},
	}
}

// readSecret uses a no-echo prompt on a real terminal and plain stdin
// otherwise (piped input, tests).
func readSecret(cmd *cobra.Command) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(cmd.ErrOrStderr(), "secret (input hidden): ")
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	line, _, _ := strings.Cut(b.String(), "\n")
	return strings.TrimSpace(line), nil
}

func newAuthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "auth <source>",
		Short: "Authorize an xoauth2 IMAP source (one-time browser flow)",
		Long: `Runs the interactive OAuth authorization for an [[vaults.ingesters]]
entry with auth = "xoauth2": prompts for your OAuth client ID/secret if the
keyring doesn't have them, opens a loopback listener, prints the URL to
approve in a browser, and stores the resulting refresh token in the OS
keyring. See docs/OAUTH-GMAIL.md for the full Gmail walkthrough.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, ic, err := resolveSourceIngester(cmd, args)
			if err != nil {
				return err
			}
			if ic.Type != "imap" {
				return fmt.Errorf("ingester %q has type %q; pkms auth is for imap sources with auth = \"xoauth2\"", ic.Name, ic.Type)
			}
			return imap.Authorize(cmd.Context(), ic.Options, v.Name, ic.Name, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}
