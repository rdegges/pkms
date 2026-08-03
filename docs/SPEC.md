# pkms — Phase 0/1 Specification (DRAFT — not yet frozen)

Status: **FROZEN 2026-08-03**. This document freezes the contracts for phases 0
and 1 and the load-bearing interfaces for phase 2. `docs/PLAN.md` is the parent
plan; where this spec is silent, the plan governs. `docs/LINT-RULES.md` is part
of this frozen spec. Changes require a new question round.

Decisions from the freeze question round (2026-08-03):

1. **Phase-1 "lint runs green" exit criterion** = lint runs to completion,
   deterministically, and *accurately reports* the real findings in the
   author's vault copy — findings are delivered as a report, never auto-fixed.
   No baseline/ratchet feature in phase 1.
2. **Built-in profiles**: `para` (clean generic PARA for new users) AND
   `rdegges` (the author's full conventions — flagship real-world example,
   shipped publicly; no personal note content, synthetic test fixtures only).

## 1. Versioning

- **Config**: `version = 1` (integer) at the top of `config.toml`. Unknown higher
  version → hard error telling the user to upgrade pkms.
- **Profile**: `schema_version = 1` in `profile.toml`. Same policy.
- **Note-type schemas**: standard JSON Schema (draft 2020-12); each has an `$id`
  of the form `pkms:profile/<profile>/<type>/v1`.
- **State files**: first line is a header record carrying `{"v":1,...}`.
- Policy: additive changes don't bump versions; breaking changes bump the integer
  and pkms must read all older versions it ever shipped (migrate forward on write).

## 2. XDG layout

Via `adrg/xdg`. All paths overridable by the standard `XDG_*` env vars.

| Purpose | Path |
| --- | --- |
| Config | `$XDG_CONFIG_HOME/pkms/config.toml` |
| User profiles | `$XDG_CONFIG_HOME/pkms/profiles/<name>/` |
| Ingest state | `$XDG_STATE_HOME/pkms/state/<vault>/<source>.ndjson` |
| Quarantine | `$XDG_STATE_HOME/pkms/failed/<vault>/<source>/<ts>-<key-hash>.json` |
| Op write-lists | `$XDG_STATE_HOME/pkms/ops/<vault>/<op-id>.json` |
| Locks | `$XDG_STATE_HOME/pkms/locks/<vault>[.<source>].lock` |
| Content-addressed assets (phase 2) | `$XDG_DATA_HOME/pkms/assets/<sha256>` |

## 3. Config format

Single TOML file. Secrets never appear in it (keychain / `PKMS_*` env /
`password_cmd` — phase 2).

```toml
version = 1

[defaults]
# optional; applies when a [[vaults]] entry omits the key
profile = "para"

[[vaults]]
name    = "personal"            # unique, [a-z0-9-]+
path    = "~/Vault/Personal"    # ~ expanded; must be absolute after expansion
profile = "para"                # built-in profile name OR absolute path to a profile dir

  [vaults.snapshot]
  remote = "git@github.com:me/vault-snapshots.git"  # optional; push-only
  # branch is always snapshots/<sanitized-hostname>; not configurable in v1

  # [[vaults.ingesters]] — phase 2; the table shape is reserved, not parsed in v1
```

- Env override: `PKMS_CONFIG` overrides the config file path (for tests and
  scripting). Individual config keys are NOT env-overridable in v1.
- Vault selection: every command takes `--vault <name>`; with one vault configured
  it's optional; with several and no flag, commands that read may run over all
  vaults, commands that write require the flag (exception: `snapshot` runs over
  all vaults by default — it's the cron entry point).

## 4. Profile contract

A profile is a **directory of data files** — no code:

```
<profile>/
├── profile.toml            # manifest (below)
├── schemas/<type>.schema.json   # JSON Schema per note type, draft 2020-12
└── templates/<type>.md          # Go text/template for new-note bodies
```

Built-in profiles (v1: `para` and `rdegges`) are embedded in the binary via
`embed.FS` and addressable by name. `pkms profile eject para <dir>` copies the embedded profile
to disk for customization (then reference it by path in config).

### profile.toml

```toml
schema_version = 1
name = "para"
description = "PARA: Projects / Areas / Resources / Archive"

# Folders scaffolded by `pkms init`. Trailing text after '#' is doc-only.
scaffold = [
  "Projects", "Areas", "Resources", "Archive",
]

# Root files created by init from templates/root/<file> if present.
root_files = ["Projects.md"]

[types.<name>]                 # one table per note type
schema   = "schemas/<name>.schema.json"
template = "templates/<name>.md"
folder   = "<Go template>"     # placement: renders to a vault-relative dir
filename = "<Go template>"     # renders to the basename (without .md)

[[indexes]]                    # index rules — drift checked by lint
file    = "Projects.md"        # vault-relative index file
lists   = "Projects/**/*.md"   # doublestar glob of notes it must link
policy  = "must-link-all"      # v1: the only policy; every match must be wikilinked from `file`
```

- **Placement/filename templates** are Go `text/template` over the note's
  validated frontmatter fields plus helpers: `{{.date | year}}`, `{{.date | month}}`,
  `{{.date | day}}` (zero-padded), `slug`, `now` is banned (determinism — dates
  come from record fields, never wall clock at render time).
- Rendered paths must stay inside the vault (reject `..`, absolute paths,
  symlink escapes) — threat-model requirement.
- **Note-type schemas** validate the frontmatter mapping only (not the body).
  `additionalProperties: true` everywhere — user-added keys are never errors.
  Each schema declares `required` keys and per-key types/enums/formats.
- The `para` built-in note types and their schemas are derived from the vault
  convention catalog (§12) — see `profiles/para/` in-repo.

## 5. Obsidian-compat semantics (normative for lint/query)

Scope: what pkms must implement to agree with Obsidian's behavior. Where Obsidian
is configurable, pkms targets Obsidian defaults ("shortest path when possible").

1. **Note identity**: a note is any `*.md` file in the vault, excluding dot-dirs
   (`.obsidian/`, `.git/`, `.trash/`) and the profile-declared attachments dir.
2. **Wikilink forms**: `[[target]]`, `[[target|display]]`, `[[target#Heading]]`,
   `[[target#^blockid]]`, embeds `![[target]]` (+ same suffixes). `target` may
   omit `.md`. Non-md embeds (`![[img.png]]`) resolve against all vault files.
3. **Resolution algorithm** for `target`:
   a. Strip heading/block suffix. Empty target (pure `[[#Heading]]`) = self-link.
   b. If it contains `/` → resolve as a vault-relative path (with and without
      `.md`).
   c. Else → match against all note **basenames**, then against frontmatter
      **`aliases`** entries.
   d. Matching is **case-insensitive** and **Unicode-NFC-normalized** (macOS
      writes NFD filenames; Obsidian matches them against NFC link text).
   e. ≥1 match → resolved (Obsidian picks one; for lint, any match = not broken).
      0 matches → **broken link**.
4. **Duplicate basenames** are legal in Obsidian but make bare-basename links
   ambiguous → lint warning (`ambiguous-link` on the link; `duplicate-basename`
   on the files).
5. **Heading/block suffixes**: if target resolves, `#Heading` must match a
   heading in the target (case-insensitive, after stripping markdown inline
   syntax); `#^blockid` must match a `^blockid` anchor. Failure → broken-anchor
   finding (warning, not error — Obsidian tolerates it).
6. **Frontmatter**: the leading `---\n...\n---` block, YAML. Obsidian tolerates
   a missing closing fence poorly; pkms treats it as a lint error (fixable).
   `aliases` and `tags` accept both string and list-of-strings forms; pkms
   normalizes to lists internally (and lint can flag the string form).
7. **Markdown links** `[text](path.md)` to vault files participate in backlink
   and broken-link analysis too (percent-decoded, relative to the linking file).
8. **Backlink graph**: directed edges from source note → resolved target for all
   wikilinks + vault-internal markdown links, including embeds.

## 6. Note model & write path (phase 0 defines; phase 2 consumes)

```go
// Note is the parsed form of one vault markdown file.
type Note struct {
    Path        string         // vault-relative, NFC-normalized
    Frontmatter *Frontmatter   // nil if absent
    Body        []byte         // raw bytes after the frontmatter block
    Doc         ast.Node       // goldmark AST of Body (lazy)
}
```

- **Frontmatter round-trip invariant**: parse → serialize with no mutations is
  byte-identical for well-formed YAML; mutations produce minimal diffs (key
  order and unrelated lines preserved). This is a test invariant.
- **Single writer**: all note creation goes through
  `writer.Write(vault, noteType, fields, body)`:
  validate fields against the type schema → render folder/filename templates →
  render body template → marshal frontmatter → **atomic write** (temp file in
  the destination dir + `rename(2)`; `O_EXCL` semantics — never overwrite an
  existing note; collision → deterministic ` 2`, ` 3`… suffix).
- Schema-invalid records quarantine to `$XDG_STATE_HOME/pkms/failed/…` as JSON
  `{record, errors, ts}` — never into the vault.

## 7. Ingester contract (interfaces frozen now; implementations are phase 2)

```go
// Record is one typed unit from a source. Ingesters emit records,
// never markdown strings.
type Record struct {
    NaturalKey string         // Message-ID / canonical URL / file SHA-256
    NoteType   string         // profile note-type name
    Fields     map[string]any // schema-validated → frontmatter
    Body       string         // markdown body (converted upstream)
    Assets     []Asset        // stored per asset policy (phase 2.5)
}

type Asset struct {
    Filename string
    SHA256   string
    Size     int64
    Open     func() (io.ReadCloser, error)
}

// EmitFunc delivers one record to the pipeline. It returns ONLY after the
// record is durable: note written atomically AND the ack appended (fsync'd)
// to the source state file — or after the record was quarantined/deduped.
// A non-nil error means the pipeline is failing; the ingester must stop.
type EmitFunc func(ctx context.Context, r Record) error

type Ingester interface {
    // Name is the registry key ("imap", "rss").
    Name() string
    // Fetch streams records. cursor is source-private resume state
    // (e.g. IMAP UIDVALIDITY+UIDNEXT) from the last committed ack; ingesters
    // MUST tolerate re-emitting already-acked records (pipeline dedups).
    Fetch(ctx context.Context, cursor Cursor, emit EmitFunc) error
}

// Registration is telegraf-style, at init():
//   ingest.Register("imap", func(cfg map[string]any) (Ingester, error) { ... })
```

**Per-record ack shape** (load-bearing): the state file
`$XDG_STATE_HOME/pkms/state/<vault>/<source>.ndjson` is append-only NDJSON.

```
{"v":1,"source":"imap:work","cursor_schema":"imap/1"}        // header
{"k":"<sha256(NaturalKey)>","note":"Resources/…​.md","ts":"…","op":"ack"}
{"k":"…","op":"quarantine","reason":"schema: missing title","ts":"…"}
{"op":"cursor","data":{"uidvalidity":123,"uidnext":456},"ts":"…"}
```

- Ack line appended + fsync **after** the note rename succeeds → crash between
  write and ack re-fetches the record; content dedup (`k` already present, or
  identical `source_id` in vault) makes the retry a no-op. Never duplicates,
  never loses.
- `source_id: <NaturalKey>` is stamped into every ingested note's frontmatter.
- Compaction: on open, if the log exceeds 10k lines it is rewritten as a
  snapshot line + tail (atomic replace).
- **Locks**: `flock` per vault (write path) and per source (ingest);
  non-blocking — a second concurrent run exits 0 with "already running".

## 8. Lint engine

### Interfaces

```go
type Severity string // "error" | "warning"

type Finding struct {
    Rule     string   `json:"rule"`
    Severity Severity `json:"severity"`
    Path     string   `json:"path"`           // vault-relative
    Line     int      `json:"line,omitempty"` // 1-based; 0 = whole file
    Message  string   `json:"message"`
    Fixable  bool     `json:"fixable"`
}

// VaultIndex is the shared parsed-vault snapshot (also used by query):
// all Notes, the resolved link graph, basename→paths map, alias map,
// per-type schema handles, and the profile.
type NoteRule interface {
    ID() string
    CheckNote(ix *VaultIndex, n *Note) []Finding
}
type VaultRule interface { // orphans, duplicate basenames, index drift
    ID() string
    CheckVault(ix *VaultIndex) []Finding
}
type Fixer interface { // implemented by rules whose findings are Fixable
    Fix(ix *VaultIndex, n *Note, f Finding) (changed bool, err error)
}
// Registered telegraf-style: lint.Register(id, factory(cfg) (Rule, error)).
```

- **Fix invariants** (tested): fix is idempotent (running twice = second run
  changes nothing); fixes never touch lines outside the finding; parse →
  serialize → parse round-trips.
- `--fix` flow: snapshot commit `pre(lint-fix)` → apply fixes (recording the
  write list to the op file) → snapshot commit `lint-fix: …`. Only findings
  from the **unambiguous** catalog set are ever `Fixable: true`.

### CLI contract

```
pkms lint [--vault v] [--fix] [--json] [--rules id,id] [--fail-on error|warning]
```

- Exit codes: `0` no findings at/above fail level (default `error`); `1`
  findings; `2` execution error. Same triple for `doctor`.
- `--json` emits `{"vault":…,"findings":[Finding…],"summary":{"error":n,"warning":n}}`.
  Human output groups by file, sorted by path then line (deterministic).
- Rule configuration (enable/disable, per-rule options like orphan exclusions)
  lives in the **profile** (`[lint]` table in profile.toml), overridable per
  vault in config (`[vaults.lint]`).

### Rule catalog

See §12 — encoded from the author's real vault conventions; each rule in the
catalog names its ID, severity, scope, semantics, fixability, and pass/fail
examples. The catalog is the normative test fixture source.

## 9. Snapshots, history, undo

- `pkms init` creates the vault git repo (if absent) and a `.gitignore`
  (`.obsidian/workspace*`, `.DS_Store`, `*.sync-conflict-*`, `.trash/`).
- `pkms snapshot [--vault v]` (default: all vaults): skip with exit 0 if repo
  is mid-merge/rebase (`MERGE_HEAD`/`rebase-merge`/`rebase-apply` present) or
  worktree clean; else `add -A` + commit `snapshot: <n> file(s) @ <RFC3339>`.
  With a remote configured: push `HEAD:refs/heads/snapshots/<sanitized-hostname>`
  (force-with-lease); push failure is a warning, never blocks the commit.
- **Mutating ops** (v1: `lint --fix`; later: ingest) wrap themselves:
  commit `pre(<op>)` → do writes, appending every touched path to
  `ops/<vault>/<op-id>.json` → commit `<op>: <summary>` with trailer
  `Pkms-Op: <op-id>`.
- `pkms undo [<op-id>|last] [--vault v]`: restore **only the op's write list**
  from the `pre(<op>)` commit (`git checkout <pre> -- <paths>`; paths created
  by the op are deleted), then commit `undo(<op>)`. Concurrent user edits to
  other files are untouched. Undo of an undo is legal (it's just another op).
- `pkms history [--vault v] [--json]`: list snapshot/op commits (id, kind,
  time, file count, op-id if any).

## 10. Query

```
pkms query [--vault v] [--type t] [--where key=value]… [--text s]
           [--backlinks path] [--orphans] [--json]
```

- All predicates AND-combine. `--where k=v`: frontmatter equality; if the field
  is a list, "contains". `k` uses dotted paths for nested maps. Values compare
  as strings after YAML-scalar normalization (dates → RFC3339 date form).
- `--text`: case-insensitive substring over the note body (not frontmatter).
- `--backlinks p`: notes whose resolved links include `p` (vault-relative path).
- Output: sorted vault-relative paths (human) or
  `{"results":[{"path":…,"frontmatter":{…}}]}` (`--json`). Deterministic order.
- Implementation: builds the same `VaultIndex` as lint; no persistent index in
  v1 (a 10k-note vault parses in well under a second; measure, don't cache).

## 11. `pkms init` & `pkms doctor`

- `pkms init --path <dir> [--vault name] [--profile para]`:
  create/register the vault — scaffold profile folders + root files, write
  `.gitignore`, `git init` + initial commit, append the `[[vaults]]` entry to
  config (creating config on first run). Idempotent: re-running on an existing
  vault only fills gaps (never overwrites existing files); `--dry-run` prints
  the plan. Refuses a non-empty dir unless `--adopt` is passed (adopt = don't
  scaffold over existing content, just register + git init + gitignore).
- `pkms doctor [--json]`: config parses & version supported; every vault path
  exists, is writable, is a git repo with ≥1 commit; profile loads and all
  schemas compile; XDG state/config dirs writable; git binary present and
  ≥ 2.30 (if the shell-out strategy is chosen — see §13); pending quarantine
  count; lock staleness. Exit codes as lint.
- `pkms version`: version, commit, profile schema versions.

## 12. Lint rule catalog (from vault conventions)

Full normative semantics + pass/fail examples: `docs/LINT-RULES.md` (53 rules,
extracted 2026-08-03 from Preferences.md §Vault Hygiene + the vault-archivist
agent definition, validated against the real vault). That file is part of this
frozen spec. Summary of the load-bearing decisions:

- **Rules are generic + parametrized; profiles instantiate them.** E.g.
  `index-links-complete` takes (index file, glob); `filename-format` takes
  (scope glob, regex); `frontmatter-schema` takes (note type → JSON Schema).
  The engine ships no vault-specific strings — those live in profile data.
- **Note-type detection is deterministic by path** (profile `[types.*]`
  scope globs), with one content trigger: `clip-summary` = Resources note
  containing `source_url` OR `date_clipped` (only ~43/115 real Resources
  pages are clips; applying the clip schema to all of Resources/ would be
  ~60% false positives).
- **Severity policy**: `error` = documented hard rule that reality upholds;
  `warning` = documented soft rule OR hard rule currently violated at scale
  (project frontmatter 16/107, session-trace frontmatter 11/12, resources
  cataloged 75/115, projects linked 60/107). Severities are per-rule
  profile config, so they can be ratcheted later.
- **Fixable set** (unambiguous + idempotent only): tab→spaces in frontmatter,
  missing closing `---`, unambiguous date re-formats, key order, kebab-case
  topics, bare-attendee→wikilink wrap, path-derivable required keys
  (`type: project`, `category`, `type: daily-brief`, `type: recipe`,
  session-trace `date`/`slug` from filename), count-drift recomputes
  (`total_items`, `recipe_count`), single-candidate wikilink repair,
  missing empty required section headings. Everything else reports.
- **Rule groups**: frontmatter/schema (19), naming/placement (13), links (7),
  index/count drift (11), structural (6). 15 conventions are explicitly
  deferred to the agent layer (judgment calls — PARA bucket choice, dedup by
  meaning, external-author decisions, etc.).
- The per-type frontmatter field tables in LINT-RULES.md are the source of
  truth for `profiles/para/schemas/*.schema.json`.
- Known live findings in the author's vault (recipe_count drift, uncataloged
  resources, broken attendee links, root junk, 74-line Now.md) are the
  expected first-run acceptance output — reported, never auto-fixed.

## 13. Dependencies (pinned 2026-08-03)

Verified against proxy.golang.org / pkg.go.dev / upstream tags (full API notes:
scratchpad `deps-report.md`, folded into implementation).

| Module | Version | Role |
| --- | --- | --- |
| github.com/yuin/goldmark | v1.8.5 | markdown AST |
| go.abhg.dev/goldmark/wikilink | v0.6.0 | `[[wikilink]]` parsing |
| github.com/goccy/go-yaml | v1.19.2 | frontmatter YAML (ordered maps, comment round-trip) |
| github.com/santhosh-tekuri/jsonschema/v6 | v6.0.2 | note-type schemas |
| github.com/knadh/koanf/v2 | v2.3.5 | config core |
| github.com/knadh/koanf/providers/file | v1.2.1 | config file provider |
| github.com/knadh/koanf/parsers/toml/v2 | v2.2.1 | TOML parser |
| github.com/adrg/xdg | v0.5.3 | XDG paths |
| github.com/zalando/go-keyring | v0.2.8 | secrets (phase 2; pinned now) |
| github.com/spf13/cobra | v1.10.2 | CLI |
| github.com/stretchr/testify | v1.11.1 | tests only |
| Go toolchain | 1.26.5 | Docker `golang:1.26.5-trixie`; CI `1.26.5` |

Decisions (with rationale):

1. **YAML = goccy/go-yaml.** `gopkg.in/yaml.v3` was archived 2025-04-01. goccy
   provides ordered maps (`UseOrderedMap`), comment round-trip, and an AST
   escape hatch — required by the frontmatter minimal-diff invariant (§6).
2. **Frontmatter parsed by manual `---` split + goccy**, not
   `go.abhg.dev/goldmark/frontmatter` — the extension hides raw bytes/offsets
   needed for minimal-diff rewriting and depends on archived yaml.v3.
3. **Git = shell out to system `git`** (argv arrays, never shell strings).
   go-git v6 is alpha-only; v5 has open perf/correctness issues (slow
   Status/Add on many small files, gitignore edge cases) on exactly the
   snapshot workload. Consequence: git ≥ 2.30 is a runtime dependency for
   `init`/`snapshot`/`undo`/`history`; `doctor` checks for it; lint/query
   work without it.
4. **testify** for assertions; test-only dependency.
5. Wikilink gotcha (binds §5/§8 implementation): the wikilink AST node exposes
   `Target`/`Fragment`/`Embed` but NOT the alias (child text nodes) and inline
   nodes carry no source segments — line numbers for link findings come from a
   small raw-source scan helper, not the AST.

## 14. Threat model requirements (phase 0/1 slice)

Phase 2 fetch/IMAP guards are specced in the plan; the slice that binds now:

- Rendered profile paths confined to the vault (no `..`, no absolute, no
  symlink escape — verify with `filepath.EvalSymlinks` containment check).
- YAML parsing with resource limits: reject frontmatter blocks > 64 KiB;
  alias/anchor expansion is bounded by the chosen YAML lib (verified in tests).
- goldmark parsing wrapped with a size cap (default: skip lint body-parse on
  files > 10 MiB with a `file-too-large` warning finding).
- All subprocess execution (git, future hooks) uses argv arrays — never a
  shell string. Untrusted strings (filenames, config values) are never
  interpolated into command lines beyond argv positions.
- `pkms` never follows symlinks out of the vault during walks; symlinked dirs
  inside the vault are traversed at most once (inode cycle guard).

## 15. Testing & CI contract

- Unit tests beside every package; the rule catalog's pass/fail examples are
  table-driven test fixtures (one golden vault per scenario under
  `internal/lint/testdata/`).
- Invariant tests: fix idempotence, frontmatter round-trip, atomic-write
  collision behavior, undo restores byte-identical content.
- `pkms lint` acceptance: runs against a **copy** of the author's real vault
  (local-only target, not CI) — first run reports findings, never fixes.
- CI (GitHub Actions): `go vet` + `golangci-lint` + `go test ./...` on
  linux-amd64; release job cross-compiles darwin/linux × arm64/amd64 (CGO off)
  and attaches binaries to GitHub Releases on tag.
- All local Go invocations run inside the official Go Docker image (host stays
  clean); the repo Makefile wraps this (`make test`, `make build`, `make lint`).

## 16. Repo layout

```
cmd/pkms/                 # cobra main
internal/config/          # koanf loading, validation
internal/vault/           # walking, Note parse, VaultIndex, atomic writer
internal/profile/         # manifest, schemas, templates, embed of para
internal/lint/            # engine + registry
internal/lint/rules/      # one file per rule
internal/snapshot/        # git ops, op write-lists, undo, history
internal/query/
internal/ingest/          # interfaces + state store ONLY (phase 2 fills in)
profiles/para/            # the embedded default profile (data files)
docs/PLAN.md docs/SPEC.md
.github/workflows/ci.yml release.yml
Makefile Dockerfile? (no—plain `docker run` in Makefile)
```
