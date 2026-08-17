#!/usr/bin/env bash
# pkms sandbox — try the pkms Claude Code plugin in COMPLETE isolation.
#
# Nothing here touches your real setup: it uses a throwaway Claude config
# dir (via CLAUDE_CONFIG_DIR — no global CLAUDE.md, no personal subagents,
# no real plugins) with ONLY the pkms plugin, plus a throwaway pkms config
# and seeded vaults. Your ~/.claude, ~/.config/pkms, and real vaults are
# never read or written.
#
# Usage: scripts/pkms-sandbox.sh
# It sets everything up and prints the exact commands to open a session and
# to verify the result afterward.
set -euo pipefail

command -v claude >/dev/null || { echo "claude CLI not found on PATH" >&2; exit 1; }
command -v pkms   >/dev/null || { echo "pkms not on PATH — install v0.4.0+ first" >&2; exit 1; }
HERE="$(cd "$(dirname "$0")" && pwd)"

ROOT="$(mktemp -d "${TMPDIR:-/tmp}/pkms-sandbox.XXXXXX")"
CLAUDE_CONFIG_DIR="$ROOT/claude"
mkdir -p "$CLAUDE_CONFIG_DIR"

echo "Setting up an isolated pkms sandbox — your real config and vaults are untouched."
echo
echo "1/2  Installing the pkms plugin into an isolated Claude config…"
CLAUDE_CONFIG_DIR="$CLAUDE_CONFIG_DIR" claude plugin marketplace add rdegges/pkms >/dev/null
CLAUDE_CONFIG_DIR="$CLAUDE_CONFIG_DIR" claude plugin install pkms@rdegges --scope user >/dev/null
echo "     installed: pkms@rdegges (in $CLAUDE_CONFIG_DIR)"

echo "2/2  Seeding throwaway vaults (a real 'inbox' + a 'decoy' the agent must not touch)…"
PKMS_ROOT="$ROOT/pkms"
SEED_OUT="$(PKMS_ACCEPT_BASE="$PKMS_ROOT" "$HERE/accept-agent-seed.sh")"
BASE="$(printf '%s\n' "$SEED_OUT" | sed -n 's/^ACCEPT_BASE=//p' | head -1)"
[ -n "$BASE" ] && [ -f "$BASE/env.sh" ] || { echo "seed failed" >&2; exit 1; }
# shellcheck disable=SC1090
. "$BASE/env.sh"

# One consolidated launch script so the session inherits BOTH isolations.
LAUNCH="$ROOT/launch-sandbox.sh"
{
  echo "#!/usr/bin/env bash"
  echo "export CLAUDE_CONFIG_DIR=\"$CLAUDE_CONFIG_DIR\""
  echo "export PKMS_CONFIG=\"$PKMS_CONFIG\""
  echo "export XDG_STATE_HOME=\"$XDG_STATE_HOME\""
  echo "export XDG_DATA_HOME=\"$XDG_DATA_HOME\""
  echo "export XDG_CONFIG_HOME=\"$XDG_CONFIG_HOME\""
  echo "export XDG_CACHE_HOME=\"$XDG_CACHE_HOME\""
  echo "exec claude"
} > "$LAUNCH"
chmod +x "$LAUNCH"

cat <<EOF

Sandbox ready. Nothing above touched your real ~/.claude, ~/.config/pkms,
or your vaults.

  Open the isolated session:

      $LAUNCH

  Inside that session, run the pkms inbox skill by name (the isolated config
  has no CLAUDE.md, so the plain phrase would work too — but naming the skill
  is unambiguous):

      /pkms:process-inbox

  When it finishes, verify the result:

      CLAUDE_CONFIG_DIR="$CLAUDE_CONFIG_DIR" \\
      PKMS_CONFIG="$PKMS_CONFIG" XDG_STATE_HOME="$XDG_STATE_HOME" \\
      XDG_DATA_HOME="$XDG_DATA_HOME" XDG_CONFIG_HOME="$XDG_CONFIG_HOME" \\
      XDG_CACHE_HOME="$XDG_CACHE_HOME" \\
      "$HERE/accept-agent-verify.sh" "$BASE"

  Throw the whole sandbox away when done:

      rm -rf "$ROOT"
EOF
