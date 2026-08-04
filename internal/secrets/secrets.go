// Package secrets resolves ingester credentials (SPEC §24). Secrets never
// live in config: OS keyring → PKMS_* env → password_cmd, in that order.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/zalando/go-keyring"
)

// Service is the keyring service name for every pkms secret.
const Service = "pkms"

// Kind names one credential slot for a source.
type Kind string

const (
	Password          Kind = "password"
	OAuthClientID     Kind = "oauth-client-id"
	OAuthClientSecret Kind = "oauth-client-secret"
	OAuthRefreshTok   Kind = "oauth-refresh-token"
)

// Account is the keyring account string: <vault>/<source>/<kind>.
func Account(vaultName, source string, kind Kind) string {
	return fmt.Sprintf("%s/%s/%s", vaultName, source, kind)
}

// EnvVar is the env override name: PKMS_<VAULT>_<NAME>_<KIND>.
func EnvVar(vaultName, sourceName string, kind Kind) string {
	up := func(s string) string {
		return strings.ToUpper(strings.NewReplacer("-", "_", ":", "_").Replace(s))
	}
	return fmt.Sprintf("PKMS_%s_%s_%s", up(vaultName), up(sourceName), up(string(kind)))
}

// Resolve finds one secret (SPEC §24 order). passwordCmd applies to the
// password kind only; pass nil otherwise. The error copy names every place
// it looked and how to store the secret.
func Resolve(vaultName, source, sourceName string, kind Kind, passwordCmd []string) (string, error) {
	acct := Account(vaultName, source, kind)
	if v, err := keyring.Get(Service, acct); err == nil && v != "" {
		return v, nil
	}
	// Keyring errors (headless: no D-Bus/Secret Service) fall through —
	// go-keyring does not degrade on its own.

	envName := EnvVar(vaultName, sourceName, kind)
	if v := os.Getenv(envName); v != "" {
		return v, nil
	}

	if kind == Password && len(passwordCmd) > 0 {
		out, err := runPasswordCmd(passwordCmd)
		if err != nil {
			return "", fmt.Errorf("password_cmd %q: %w", passwordCmd[0], err)
		}
		if out != "" {
			return out, nil
		}
	}

	hint := ""
	if kind == Password && len(passwordCmd) == 0 {
		hint = ", or set password_cmd on the ingester"
	}
	return "", fmt.Errorf(
		"no %s found for %s: not in the OS keyring (account %q) and $%s is unset%s.\nStore it with: pkms secret set %s %s",
		kind, source, acct, envName, hint, sourceName, kind)
}

// Store writes a secret to the OS keyring.
func Store(vaultName, source string, kind Kind, value string) error {
	return keyring.Set(Service, Account(vaultName, source, kind), value)
}

// Delete removes a secret from the OS keyring. existed is false when there
// was nothing stored (a no-op, still nil error — rm is idempotent).
func Delete(vaultName, source string, kind Kind) (existed bool, err error) {
	err = keyring.Delete(Service, Account(vaultName, source, kind))
	if errors.Is(err, keyring.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// runPasswordCmd executes the argv array directly — never a shell (SPEC
// §14) — and returns the first line of stdout. The child inherits the
// environment with every PKMS_* var scrubbed, so a password helper can't
// read one source's secret while resolving another's.
func runPasswordCmd(argv []string) (string, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = os.Stderr
	cmd.Env = scrubbedEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line), nil
}

func scrubbedEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		if strings.HasPrefix(kv, "PKMS_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// ParseKind validates a CLI-supplied kind name.
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case Password, OAuthClientID, OAuthClientSecret, OAuthRefreshTok:
		return Kind(s), nil
	}
	return "", fmt.Errorf("unknown secret kind %q (valid: password, oauth-client-id, oauth-client-secret, oauth-refresh-token)", s)
}
