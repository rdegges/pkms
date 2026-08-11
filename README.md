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

Homebrew:

```sh
brew install rdegges/tap/pkms
```

Or grab an archive from [Releases](https://github.com/rdegges/pkms/releases)
(macOS/Linux, arm64/amd64), or build from source (Go 1.26+):
`go install github.com/rdegges/pkms/cmd/pkms@latest`

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

## Ingest: web pages, feeds, and email → notes

One-shot: throw a URL (or local file) at your vault and it lands as a clip
note in your inbox folder, converted to clean markdown:

```sh
pkms ingest https://example.com/great-article
# ingested → _Inbox/2026-08-03T142530+0000 - Great Article.md

pkms ingest https://example.com/great-article    # run it again —
# already ingested → _Inbox/2026-08-03T142530+0000 - Great Article.md
```

Scheduled: configure pull ingesters once, then let cron call `pkms ingest`.
Every record is deduplicated by a natural key (email Message-ID, feed GUID,
canonical URL), acknowledged only after its note is durably written, and
quarantined outside the vault if it fails schema validation. Re-runs and
crash recoveries never duplicate a note and never lose one.

```toml
[[vaults.ingesters]]
type = "rss"
name = "hn"
url  = "https://hnrss.org/frontpage"

[[vaults.ingesters]]
type     = "imap"
name     = "fastmail"
host     = "imap.fastmail.com"
username = "you@fastmail.com"
auth     = "password"          # app password — see below
```

Secrets never live in config: pkms checks the OS keyring
(`pkms secret set fastmail password`), then `PKMS_*` env vars, then an
optional `password_cmd` argv array (`["op", "read", "op://…"]`). IMAP is
strictly read-only — pkms never marks mail as read, never deletes, and
resumes UIDVALIDITY-aware. Gmail works via app passwords or your own OAuth
client (`pkms auth gmail` — honest ~30-minute walkthrough in
[docs/OAUTH-GMAIL.md](docs/OAUTH-GMAIL.md)).

Fetches are hardened by default: private/link-local addresses are refused
(SSRF guard on resolved IPs), redirects/sizes/times are capped, and content
is dispatched on sniffed bytes, never on the server's Content-Type header.

**Every content type lands — no refusals.** A PDF becomes a note with its
text extracted (behind a sandboxed, killed-on-timeout subprocess, so a
malformed file can't crash or hang the run); audio and video become asset
notes you can enrich with your own tools via `probe_cmd`/`transcribe_cmd`
(ffprobe, whisper — argv arrays, never a shell); anything else lands as a
generic asset. Attachments — pushed files and email attachments alike —
are stored by size: small ones copied into the vault's `Attachments/` and
wikilinked, large ones content-addressed outside the vault (they bloat git
and break Obsidian Sync). `pkms doctor` reports any attachment that has
moved or gone missing.

> Note on PDF text quality: extraction uses a pure-Go library that does not
> yet decode subset/CID fonts (common in Word- and browser-generated PDFs);
> those land as an asset note with the PDF stored but no extracted text. A
> stronger extractor is tracked as a follow-up.

## Agents: the judgment layer

pkms is the deterministic substrate; it never guesses. The taste-dependent
work — deciding where a note belongs, whether two notes are the same thing,
what a clean inbox looks like — lives in an **agent layer** that drives pkms
through its safe, undoable primitives. It ships in this repo as a Claude
Code plugin: two agents (a write-capable **archivist** and a read-only
**librarian**) and two skills (`cli` and `process-inbox`).

Install it in Claude Code with two commands:

```
/plugin marketplace add rdegges/pkms
/plugin install pkms@rdegges
```

Then just say what you want:

> process my inbox

The archivist reads your vault's structure with `pkms profile show`, files
each captured note where it belongs, and verifies the result with
`pkms lint` — snapshotting first so any change is reversible. It treats note
content as data, never as instructions, and leaves genuinely ambiguous notes
where they are rather than guess. The librarian answers questions
("who is X", "what did we decide about Y") citing only notes `pkms query`
actually returns.

Not on Claude Code? `pkms mcp` runs a read-only Model Context Protocol
server over stdio, exposing `query`, `lint`, and `profile_show` — the same
deterministic JSON — to any MCP host.

## Status

Phases 0–3 are complete (init, doctor, lint, snapshot/undo/history, query,
ingest with PDF/audio/video/attachment handlers, and the agent layer), built
and tested against a real 989-note production vault. See `docs/PLAN.md` and
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
