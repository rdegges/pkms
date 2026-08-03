# pkms — Project Plan (v4, 2026-08-03)

Open-source deterministic manager for Obsidian-compatible markdown knowledge vaults.
Tagline: scaffold, ingest, lint, snapshot — LLM judgment at the edges, never in the core.
Visual version of this plan (same content, review-friendly): https://claude.ai/code/artifact/6c3c6fa0-8188-4774-9f9d-7a5fc2249e12
v4 folds in accepted findings from an adversarial cross-model review (Codex/GPT-5.5); rejected findings are listed at the bottom.

## Positioning

Four pillars no existing tool combines:

1. **Multi-vault management** — one config, many vaults (Personal, Work), each with its own profile and ingesters.
2. **Organization profiles** — PARA ships as default; profiles are data files, not code.
3. **Pluggable ingesters** — pull-based, scheduled, idempotent; plus push-style one-shot ingest.
4. **Deterministic lint** — CI-grade vault health. Same input, same output.

**Philosophy fence:** if a feature needs a model to be correct, it lives in the agent layer (shipped agent definitions/skills) or a user hook — never the binary. The binary stays deterministic forever.

Landscape (Aug 2026): closest competitor is claude-obsidian (~10k stars: PARA + lint + AI filing skills, but single-vault, no ingester framework, no scheduled pipelines). Others: obsidian-mind (~4k), official Obsidian CLI + kepano/obsidian-skills (requires GUI app), basic-memory (~3.6k). Reuse rather than rebuild: lychee (external links), markdownlint rule catalog (reference), karakeep API (future source).

## Design decisions

1. **Language:** Go, single static binary. goldmark (+wikilink/frontmatter extensions), santhosh-tekuri/jsonschema, go-imap v2, koanf, adrg/xdg, go-keyring, cobra. Accepted trade-off: write our own lint rule walker over the goldmark AST instead of inheriting remark-lint.
2. **Profiles are data, not code:** an org system = directory of folder templates + JSON Schema files per note type (person, meeting, clip, project, asset). The profile contract includes folder-placement and filename templates and index rules — placement logic is part of the profile format, not implied behavior. PARA built in; users drop in Zettelkasten/Johnny.Decimal as files.
3. **Telegraf-style in-process registries** for both ingesters and lint rules. Ingesters **stream** records with **per-record acknowledgement**: a record is acked into state only after its note is written atomically. Crash mid-batch recovers by re-fetch + dedup — never duplicates, never loses. Per-vault and per-source locks so overlapping cron runs can't double-ingest. One malformed record quarantines without blocking its batch. The protocol-like shape keeps a later subprocess escape hatch (NDJSON over stdout) plausible; not built in v1.
4. **Deterministic core, LLM edges:** the binary owns everything rule-decidable (schemas, links, naming, index drift, snapshots, retrieval). Shipped agent definitions (archivist = filing/summarizing/tagging; librarian = NL Q&A) do judgment on top, calling the binary's `--json` outputs.
5. **Write path (prevention over repair):** ingesters emit typed records, never markdown strings. One writer: validate against the note-type JSON Schema → marshal frontmatter with a YAML marshaller → render profile template → atomic write (temp file + rename). Schema failures are quarantined in `$XDG_STATE_HOME/pkms/failed/` — OUTSIDE the vault, so malformed or sensitive raw content never syncs.
6. **Dedup/idempotency:** natural key per record (email Message-ID with hashed-header fallback, canonical URL, file SHA-256) → SHA-256 → per-source state file in `$XDG_STATE_HOME` (mbsync-style, UIDVALIDITY-aware for IMAP). Key stamped into note frontmatter as `source_id`. Re-runs are no-ops.
7. **Secrets:** never in config. OS keychain via go-keyring; `PKMS_*` env vars and `password_cmd = "op read ..."` overrides. IMAP auth: XOAUTH2 with bring-your-own OAuth client ID (~30-minute documented setup — consent screens are the slow part); plain passwords for Fastmail/self-hosted. JMAP deferred.
8. **Config:** single TOML at `$XDG_CONFIG_HOME/pkms/config.toml` with `[[vaults]]` array-of-tables and per-vault `[[vaults.ingesters]]`.
9. **Threat model is a spec requirement, not an afterthought:** SSRF guards + redirect/size/time limits on fetch; content sniffing instead of trusting Content-Type headers; parser resource limits; arg-safe execution for `password_cmd`/`transcribe_cmd` (argv arrays, no shell strings).

## Snapshots & remotes

- Local git required: `pkms init` creates the repo. `pkms snapshot` = add -A + commit `snapshot: N file(s) @ <iso>`; skips if no changes or mid-merge/rebase; maintains .gitignore for `.obsidian/workspace*`, `.DS_Store`, sync-conflict files.
- Hourly (cron/launchd), AND every mutating command commits **before and after** so each operation's diff is isolated.
- `pkms undo` reverts only the files that operation touched (from its own write list) — never concurrent user edits. `pkms history` lists snapshots.
- Remote optional and **push-only, never pulls**: each machine pushes to its own branch `snapshots/<hostname>` (sanitized). No merges ever. Documented restore recipe (`git checkout snapshots/<host> -- <path>`). Obsidian Sync stays the transport; git stays the time machine. Git-as-transport mode deliberately deferred.

## Query

- `pkms query`: deterministic retrieval — **field filters + full-text + backlinks**, `--json` for agents. Full query grammar deferred until demand shows. Phase 1, alongside lint (shares the link-graph index).
- NL answering is NOT in the binary: the phase-3 librarian agent calls `pkms query --json` and cites the real paths it returned.

## Ingest

- `pkms ingest <path-or-url>`: one push entry point, dispatched on sniffed MIME type. `text/html` → readability (go-readability) → markdown (html-to-markdown) → Inbox note. Static fetch only (no headless browser). `application/pdf` → text extraction + PDF as asset. `audio/*`, `video/*` → asset note with deterministic metadata. Unknown → generic asset note with hash + mime, never a refusal.
- `pkms ingest` (no args): run all configured pull ingesters (IMAP, RSS) or one by name. Scheduled via cron.
- Asset storage policy: under threshold (default 25MB) → vault `Attachments/`, wikilinked; over → reference in place or content-addressed store outside the vault (`~/.local/share/pkms/assets/<sha256>`). Rationale: big binaries bloat git history and hit Obsidian Sync limits. `doctor` checks dangling asset refs; git-lfs opt-in only.
- Transcription hook: `transcribe_cmd` in config lets pkms orchestrate the user's own tool (whisper etc.) and append a `## Transcript` section. Same pattern for `ffprobe`-style metadata helpers. Unconfigured → asset note still lands, with a hint.

## Lint

- `pkms lint`: frontmatter schema validation, broken wikilinks, orphans (with exclusions for indexes/templates/attachments), naming rules, index/count drift, log format. Exit codes + `--json`.
- `--fix` applies ONLY unambiguous, idempotent repairs (commit before and after): tab indentation, missing closing `---`, unambiguous date formats, key order, malformed-but-single-repair wikilinks, missing required keys with derivable values. Semantic calls — tag-case merges, heading levels, ambiguous dates, multi-repair wikilinks — are findings, never guesses. Idempotence is a test invariant (fix twice = no-op; parse→serialize→parse round-trip).
- Escalation ladder: deterministic fix → deterministic flag → agent judgment (archivist reads `lint --json`) → human.

## Phases

- **Phase 0 — foundation.** FIRST: the spec — profile contract (folder/filename templates, note-type schemas, index rules), Obsidian-compat semantics (wikilink resolution, aliases, embeds, duplicate basenames, case sensitivity), versioning for config/schemas. Then: repo scaffold, multi-vault TOML config, XDG paths, `pkms init` (PARA scaffold + git init + schemas), `pkms doctor`. CI: release binaries macOS/Linux arm64+amd64. **Exit:** a friend installs one binary and scaffolds a working PARA vault.
- **Phase 1 — lint + query + snapshot.** Rule engine with real rules encoded from the author's vault conventions (`~/Vault/Personal/Preferences.md` §Vault Hygiene + `~/.claude/agents/vault-archivist.md`); `pkms query` (field filters/text/backlinks); `pkms snapshot` + optional push-only remote + `pkms undo`/`history`. **Exit:** lint runs green hourly in cron; zero LLM lint runs.
- **Phase 2 — ingest.** Registry + state layer with per-record ack; `pkms ingest <path-or-url>` (MIME dispatch, HTML handler); IMAP (XOAUTH2 + app passwords, UIDVALIDITY-aware); RSS. `type: asset` schema lands in the PARA profile now. **Exit:** scheduled email ingest runs a week with zero duplicate notes.
- **Phase 2.5 — media handlers.** PDF first, then audio/video via the dispatch + asset schema from phase 2.
- **Phase 3 — agent layer.** Shipped vault-agnostic agent definitions (archivist, librarian) + skills calling `lint --json` / `query --json` / `ingest`; optional MCP server. **Exit:** "process my inbox" works on a fresh vault with no hand-written prompts.

## Risks

- claude-obsidian adds ingesters → moat is architecture (multi-vault, scheduled deterministic pipelines, CI-grade lint); ship phase 1 fast.
- IMAP OAuth friction → honest ~30-min BYO-client docs; app passwords for Fastmail/self-hosted.
- Scope creep toward "AI PKM app" (chat UI, RAG, embeddings) → the philosophy fence.
- Hostile inputs everywhere (email, URLs, PDFs, hooks) → threat model in the spec (decision 9).

## Rejected review findings (do not re-litigate without new evidence)

From the Codex adversarial review, considered and rejected:
- **"Start with one vault."** Multi-vault is a `[[vaults]]` loop over the same code path — near-zero cost and a core differentiator.
- **"Use raw git until undo semantics exist."** The porcelain exists because the target users won't use raw git; undo semantics are defined instead (op-scoped write lists).
- **"`git add -A` captures unrelated edits."** In a vault, user edits aren't unrelated; whole-state capture is the point. The undo design addresses the only real concern.
- **"Push-only branches aren't restoration."** They are, via the documented restore recipe; no redesign needed.
