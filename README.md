# pkms

Deterministic manager for Obsidian-compatible markdown knowledge vaults.
Scaffold, lint, snapshot, query — LLM judgment at the edges, never in the core.

pkms is a single static binary that treats your vault like a codebase:

- **`pkms init`** scaffolds an organized vault (PARA by default) and puts it
  under git so every change is recoverable.
- **`pkms lint`** is CI-grade vault health: frontmatter schemas, broken
  wikilinks, orphans, naming rules, index drift. Same input, same output,
  exit codes and `--json` for automation. `--fix` applies only unambiguous,
  idempotent repairs — and wraps them in commits so `pkms undo` can revert
  exactly what it touched.
- **`pkms snapshot`** commits your whole vault (hourly from cron, if you
  like) and can push to a per-machine branch on a remote — push-only, never
  a sync engine.
- **`pkms query`** is deterministic retrieval: frontmatter filters,
  full-text, backlinks. `--json` output is built for agents to cite real
  paths instead of hallucinating.

**The philosophy fence:** if a feature needs an LLM to be correct, it does
not belong in this binary. pkms is the deterministic substrate; agents (your
own, or the definitions this project will ship in a later phase) do judgment
on top of its `--json` outputs.

## Install

Grab a binary from [Releases](https://github.com/rdegges/pkms/releases)
(macOS/Linux, arm64/amd64):

```sh
curl -Lo pkms https://github.com/rdegges/pkms/releases/latest/download/pkms-darwin-arm64
chmod +x pkms && mv pkms ~/.local/bin/
```

Or build from source (Go 1.26+): `go install github.com/rdegges/pkms/cmd/pkms@latest`

Snapshots need `git` (≥ 2.30) on your PATH. Everything else works without it.

## Five minutes to a working vault

```sh
# 1. Scaffold a PARA vault (Projects / Areas / Resources / Archive) + git repo
pkms init --path ~/Vault

# 2. Check everything is healthy
pkms doctor

# 3. Point Obsidian at ~/Vault and take notes

# 4. Lint it whenever you like (or hourly, from cron)
pkms lint

# 5. Snapshot on a schedule — every change is a commit you can dig out later
pkms snapshot
```

Already have a vault? `pkms init --path ~/MyVault --adopt` registers it
without touching your content.

## Profiles: your organization system as data

A profile is a directory of data files — folder layout, JSON Schemas per
note type, filename templates, lint configuration. No code.

- `para` — clean generic PARA. The default.
- `rdegges` — the author's production conventions (PARA + work/personal
  domain split + People/ + Meetings/), shipped as a real-world example of
  how far the format goes: 50+ lint rules, per-type schemas, index-drift
  checks.

```sh
pkms profile list
pkms profile eject para ~/my-profile   # customize, then point config at it
```

Config lives at `$XDG_CONFIG_HOME/pkms/config.toml` (default
`~/.config/pkms/config.toml`) — one file, many vaults:

```toml
version = 1

[[vaults]]
name    = "personal"
path    = "~/Vault"
profile = "para"

  [vaults.snapshot]
  remote = "git@github.com:you/vault-snapshots.git"  # optional, push-only
```

## Time machine

```sh
pkms history            # every snapshot and operation
pkms lint --fix         # fixes are wrapped in commits with a write-list
pkms undo last          # reverts exactly the files that operation touched —
                        # your concurrent edits are never rolled back
```

## Status

Phases 0–1 (init, doctor, lint, snapshot/undo/history, query) are built and
tested against a real 989-note production vault. Next up: pull-based
ingesters (email, RSS, URL clipping) with per-record acknowledgement — the
interfaces are already frozen in `internal/ingest`. See `docs/PLAN.md` and
`docs/SPEC.md`.

## Development

Everything runs in Docker so your host stays clean:

```sh
make test    # go test ./...
make lint    # vet + gofmt
make build   # linux binary in dist/
```

The spec of record is `docs/SPEC.md`; lint rule semantics live in
`docs/LINT-RULES.md`.

## License

MIT
