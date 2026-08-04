package secrets

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func TestEnvVar(t *testing.T) {
	require.Equal(t, "PKMS_PERSONAL_FASTMAIL_PASSWORD", EnvVar("personal", "fastmail", Password))
	require.Equal(t, "PKMS_MY_VAULT_WORK_MAIL_OAUTH_REFRESH_TOKEN", EnvVar("my-vault", "work-mail", OAuthRefreshTok))
}

func TestResolveOrder(t *testing.T) {
	keyring.MockInit()
	t.Setenv("PKMS_V_S_PASSWORD", "from-env")

	// Keyring wins over env.
	require.NoError(t, Store("v", "imap:s", Password, "from-keyring"))
	got, err := Resolve("v", "imap:s", "s", Password, nil)
	require.NoError(t, err)
	require.Equal(t, "from-keyring", got)

	// Env when the keyring has nothing.
	require.NoError(t, Delete("v", "imap:s", Password))
	got, err = Resolve("v", "imap:s", "s", Password, nil)
	require.NoError(t, err)
	require.Equal(t, "from-env", got)
}

func TestResolvePasswordCmd(t *testing.T) {
	keyring.MockInit()
	got, err := Resolve("v", "imap:s", "s", Password, []string{"echo", "cmd-secret"})
	require.NoError(t, err)
	require.Equal(t, "cmd-secret", got, "argv exec, first stdout line")
}

func TestResolveNotFoundCopy(t *testing.T) {
	keyring.MockInit()
	_, err := Resolve("vault", "imap:mail", "mail", Password, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), `account "vault/imap:mail/password"`)
	require.Contains(t, err.Error(), "$PKMS_VAULT_MAIL_PASSWORD")
	require.Contains(t, err.Error(), "pkms secret set mail password")
	require.Contains(t, err.Error(), "password_cmd")
}

func TestResolveKeyringErrorFallsThrough(t *testing.T) {
	keyring.MockInitWithError(errors.New("no D-Bus session"))
	t.Setenv("PKMS_V_S_PASSWORD", "env-wins")
	got, err := Resolve("v", "imap:s", "s", Password, nil)
	require.NoError(t, err, "headless keyring failure must degrade to env")
	require.Equal(t, "env-wins", got)
}

func TestParseKind(t *testing.T) {
	for _, ok := range []string{"password", "oauth-client-id", "oauth-client-secret", "oauth-refresh-token"} {
		_, err := ParseKind(ok)
		require.NoError(t, err)
	}
	_, err := ParseKind("passw0rd")
	require.ErrorContains(t, err, `unknown secret kind "passw0rd"`)
}
