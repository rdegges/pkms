# pkms — Phase 0/1/2 Specification

Status: §1–§16 **FROZEN 2026-08-03** (phases 0/1 and the load-bearing phase-2
interfaces). §17–§27 (phase 2 — ingest) **FROZEN 2026-08-03** after the
phase-2 question round:

1. **para profile**: gains a top-level `Inbox/` folder (scaffolded) and a
   minimal `clip` note type targeting it — all ingested notes land in
   `Inbox/` for later sorting (documented amendment to the phase-0 "para
   ships no typed notes" decision).
2. **Non-HTML push inputs**: exit 2 with honest "lands in phase 2.5" copy —
   no partial asset policy in phase 2.
3. **Secrets UX**: ship both `pkms secret set|rm` and `pkms auth <source>`.
4. **Email provenance**: `source: mid:<message-id>` (RFC 2392); the
   raw-clip/clip `source` schema pattern is `^(https?://|mid:|file://)`.
`docs/PLAN.md` is the parent plan; where this spec is silent, the plan
governs. `docs/LINT-RULES.md` is part of this frozen spec. Changes to frozen
sections require a new question round; deviations discovered later get
documented amendments, never silent drift.

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
- **Note-type schemas**: standard JSON Schema (draft 2020-12); compiled under
  the resource URL `pkms://profile/<profile>/<type>/v1`.
- **State files**: first line is a header record carrying `{"v":1,...}`.
- Policy: additive changes don't bump versions; breaking changes bump the integer
  and pkms must read all older versions it ever shipped (migrate forward on write).

## 2. XDG layout

Implemented in-tree (`internal/paths`), following the XDG basedir spec
literally on every Unix platform — `$XDG_*_HOME` when set, else `~/.config`,
`~/.local/state`, `~/.local/share`. (Amended: `adrg/xdg` was dropped because
it diverges to `~/Library/Application Support` on macOS, breaking the
documented config path below.)

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

[[types]]                      # ordered array — order IS classification
name     = "<name>"            # precedence (first matching type wins;
scope    = ["<glob>", ...]     # content-triggered types precede fallbacks)
require_any_key = ["k1", "k2"] # optional content trigger (any key present)
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
- Per the freeze-round decision, the generic `para` profile ships **no typed
  notes** (profile-agnostic rules only); the full note-type catalog derived
  from the vault conventions (§12) ships in the `rdegges` built-in — see
  `profiles/` in-repo.

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

- `pkms init --path <dir> [--vault name | --name name] [--profile para]`:
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
| github.com/bmatcuk/doublestar/v4 | v4.10.0 | scope/glob matching |
| golang.org/x/text | v0.40.0 | Unicode NFC normalization (§5) |
| github.com/pelletier/go-toml/v2 | v2.3.1 | profile manifest decode (also koanf's TOML backend) |
| github.com/zalando/go-keyring | v0.2.8 | secrets (phase 2; pinned now) |
| github.com/spf13/cobra | v1.10.2 | CLI |
| github.com/stretchr/testify | v1.11.1 | tests only |
| Go toolchain | 1.26.5 | Docker `golang:1.26.5-trixie`; CI `1.26.5` |

Decisions (with rationale):

0. **adrg/xdg dropped** (was pinned v0.5.3) — see §2 amendment; XDG paths are
   ~30 lines in-tree with the exact documented semantics on macOS.
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
- `pkms` never follows symlinks during walks: symlinked files are skipped and
  symlinked directories are not traversed in v1 (deterministic; documented
  divergence — Obsidian does index through them). The write path additionally
  verifies the `EvalSymlinks`-resolved destination stays inside the resolved
  vault root.

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

---

# Phase 2 — Ingest (§17–§27)

Research basis: scout briefs 2026-08-03 (dependency pins verified against
proxy.golang.org; IMAP semantics against RFC 9051/4549/5322; fetch limits
against lychee/miniflux/karakeep shipped defaults).

## 17. Ingest pipeline semantics

The pipeline is the only consumer of the frozen §7 contract. It owns dedup,
durability ordering, quarantine, cursor persistence, and snapshot wrapping;
ingesters only fetch and emit.

**Run flow, per (vault, source):**

1. Open the source state store (`OpenState`) — takes the per-source flock.
   Lock already held → print `<source>: already running` and exit 0 (§7).
2. `snapshot.Begin(vault, "ingest")` — `pre(ingest)` commit isolates the run.
3. Build the vault's **source-id set**: scan the `VaultIndex` for all
   frontmatter `source_id` values. This closes the crash window between note
   rename and ack (see recovery below).
4. Construct the ingester from its registered factory and config table;
   call `Fetch(ctx, cursor, emit)` with the last persisted cursor.
5. `emit(record)` processing order (load-bearing):
   a. `NaturalKey == ""` → pipeline error (ingester bug); stop the run.
   b. `state.Seen(key)` → dedup no-op; count and return nil.
   c. `source_id` already present in the vault's source-id set → **ack
      repair**: append an ack for the key with the existing note path, count
      as deduped, return nil. (This is the §7 "identical source_id in vault"
      recovery: a prior run crashed after rename, before ack.)
   d. Stamp `source_id: <NaturalKey>` into `Fields`. Ingesters MUST NOT set
      `source_id` themselves; the pipeline owns it.
   e. `writer.Write(...)`. On `ErrQuarantined`: the quarantine file is
      already durable (writer fsyncs it) → `state.Quarantine(key, reason)`,
      count, return nil — one malformed record never blocks its batch.
   f. On success: `op.Record(relPath)`, then `state.Ack(key, relPath)`.
      Ack strictly after rename (§7 durability invariant).
6. `Fetch` returns nil → persist the cursor: the `Cursor` map is passed by
   reference; the ingester mutates it as it progresses, and the pipeline
   calls `state.SetCursor` once after a clean return. `Fetch` returns an
   error → cursor is NOT persisted (acks already durable; the next run
   re-fetches from the old cursor and dedup no-ops the overlap).
7. `op.End("<source>: N new, M deduped, Q quarantined")`, or `op.Discard()`
   when the run wrote nothing. (Amended 2026-08-03 at implementation: §9's
   op machinery prepends the kind to commit subjects, so passing
   `ingest(<source>): …` would have produced `ingest: ingest(<source>): …`;
   the commit subject is `ingest: <source>: N new, M deduped, Q
   quarantined`.)
8. Per-source summary on stdout:
   `<source>: N new, M deduped, Q quarantined` (exact copy; e2e asserts it).

**Crash-recovery invariant (tested):** kill the pipeline at any point; the
re-run never duplicates a note and never loses a record. The two windows:
crash before rename → nothing durable, re-fetch re-emits; crash between
rename and ack → step 5c ack-repairs from the vault's source-id set.

**Exit codes** (mirrors lint/doctor): `0` clean run (including all-dedup
no-ops and lock-held early exit); `1` ≥ 1 record quarantined this run;
`2` execution error (config, auth, network hard failure, pipeline error).
Scheduled runs therefore alert on nonzero exits.

**Wall-clock bound:** each source run carries a context deadline (default
`timeout = "10m"` per source, config-overridable) so a wedged server can
never hang cron.

**`--json`** emits `{"vault":…,"sources":[{"source":…,"new":n,"deduped":n,
"quarantined":n,"cursor_reset":bool,"notes":[paths…]}]}` — deterministic
order (config order).

## 18. Config: `[[vaults.ingesters]]`

```toml
[[vaults]]
name = "personal"
# …

  [[vaults.ingesters]]
  type = "rss"                          # registry key
  name = "hn"                           # unique per vault, [a-z0-9-]+
  url  = "https://hnrss.org/frontpage"

  [[vaults.ingesters]]
  type     = "imap"
  name     = "fastmail"
  host     = "imap.fastmail.com"        # implicit TLS; port = 993 default
  username = "randall@example.com"
  auth     = "password"                 # "password" | "xoauth2"
  mailbox  = "INBOX"                    # default "INBOX"
  # port         = 993
  # batch        = 200                  # max messages per run
  # timeout      = "10m"                # per-run wall clock
  # password_cmd = ["op", "read", "op://Private/fastmail/password"]
  # enabled      = true                 # default true
```

- Source identity: `<type>:<name>` (e.g. `imap:fastmail`). State file:
  `state/<vault>/<type>-<name>.ndjson` (colon is unsafe on some filesystems).
- `name` must match `[a-z0-9-]+` and be unique within the vault; `type` must
  name a registered ingester — violations are config errors that name the
  offending entry and list registered types.
- **Strict keys:** each factory validates its table and errors on unknown
  keys (typo protection: `usrname =` must fail loudly, not silently ignore).
- `password_cmd` is an argv **array**, executed directly — never a shell
  string (§14). A bare string value is a config error telling the user to
  use array syntax.

## 19. CLI contract

```
pkms ingest [<path-or-url>] [--vault v] [--source name] [--json]
pkms auth <source> [--vault v]          # interactive OAuth bootstrap (§24)
pkms secret set|rm <source> <kind> [--vault v]   # keyring helper (§24)
```

- **Push mode** (`pkms ingest <path-or-url>`): one-shot ingest of a URL
  (http/https only) or an existing regular file. Runs the same pipeline with
  source `adhoc`: repeated ingest of the same URL/file is a dedup no-op and
  prints the existing note's path
  (`already ingested → Resources/Clips/Inbox/….md`). With multiple vaults,
  push mode requires `--vault`.
- **Pull mode** (no argument): run all `enabled` configured ingesters for
  the selected vault; `--source <name>` restricts to one. With multiple
  vaults and no `--vault`, pull mode runs over ALL vaults (like `snapshot` —
  it is the cron entry point). No ingesters configured → exit 2 with copy
  that shows a minimal `[[vaults.ingesters]]` example and mentions
  `pkms ingest <url>`.
- `--source` with an unknown name → exit 2, listing configured source names.

## 20. MIME dispatch (push mode)

Sniff with `http.DetectContentType` over the first 512 bytes of the fetched
body (never trust the `Content-Type` header alone; §14). The header is
consulted only for charset hints via `x/net/html/charset.DetermineEncoding`.

| sniffed type | handler | lands in |
| --- | --- | --- |
| `text/html`, `application/xhtml+xml` | readability → markdown | raw-clip note |
| `text/plain` (incl. markdown) | body verbatim (fenced only if sniff says binary-ish text? no — verbatim) | raw-clip note |
| everything else | **phase 2.5** — exit 2: `unsupported content type <t> (PDF/audio/video land in phase 2.5); nothing was written` | — |

*(Question-round decision 2: the plan's "never a refusal" dispatch completes
in phase 2.5 with the asset policy; until then the error is honest and
writes nothing.)*

- **HTML pipeline:** hardened fetch (§21) → charset-decode to UTF-8
  (`charset.NewReader`) → `readability/v2` extract (base URL = fetched URL)
  → `html-to-markdown/v2` convert → `Record{NoteType: "raw-clip"}` with
  fields: `title` (extracted, fallback: URL), `source` (input URL,
  verbatim), `created` (fetch time, RFC3339 with offset), `tags: [clip]`,
  plus `fetched_url` when the post-redirect URL differs from the input.
- **Local file:** read (10 MiB cap), sniff, same dispatch; `source` is
  `file://<absolute path>`; NaturalKey = file content SHA-256.
- **Canonical-URL NaturalKey** (URL push + RSS fallback): lowercase scheme
  and host, strip default port (`:80`/`:443`), strip fragment. Nothing else
  (query params, trailing slashes preserved — tracking-param judgment is
  agent-layer). The `source` FIELD always stores the user/feed-supplied URL
  verbatim (karakeep/lychee precedent); normalization is key-only.

## 21. Fetch hardening (threat model, exact numbers)

One shared hardened HTTP client for all outbound fetches (push, RSS):

| control | value | precedent |
| --- | --- | --- |
| connect timeout | 3 s | fail fast on filtered private IPs |
| total request deadline | 20 s | lychee `--timeout` + miniflux default |
| max redirects | 5 | lychee default |
| max body, HTML page | 10 MiB | below miniflux's feed cap; articles ≪ 1 MiB |
| max body, feed | 15 MiB | miniflux `HTTP_CLIENT_MAX_BODY_SIZE` |
| max body, IMAP message part | 10 MiB | consistency with HTML |
| feed items per run | 500 (parse, then cap; **log the dropped count**) | defensive; real feeds < 100 |
| sniff window | 512 B | stdlib `DetectContentType` |

- **SSRF guard:** `net.Dialer.Control` hook on the shared `Transport` — the
  check runs on the **resolved** IP post-DNS, pre-connect (closes
  TOCTOU/DNS-rebinding), and therefore re-runs on every redirect hop and
  every retry. Before range checks, `netip.Addr.Unmap()` v4-in-v6 forms and
  re-check the embedded v4 (`::ffff:0:0/96`, `64:ff9b::/96`, `2002::/16` —
  the classic bypass).
- **Deny (v4):** `0.0.0.0/8`, `10/8`, `100.64/10`, `127/8`, `169.254/16`
  (cloud metadata), `172.16/12`, `192.0.0/24`, `192.0.2/24`, `192.88.99/24`,
  `192.168/16`, `198.18/15`, `198.51.100/24`, `203.0.113/24`, `224/4`,
  `240/4`, `255.255.255.255/32`.
  **Deny (v6):** `::1/128`, `::/128`, `fc00::/7`, `fe80::/10`, `ff00::/8`.
- **Schemes/ports:** `http`/`https` only; ports 80/443 plus a port given
  explicitly in the configured/pushed URL.
- Refusals and cap breaches are execution errors whose copy names the limit
  and the URL (e.g. `refusing to fetch <url>: resolves to private address
  10.0.0.5`).
- Body caps enforced with `http.MaxBytesReader` BEFORE any parse. Parse
  calls (readability, markdown conversion, feed decode) additionally run
  under the request's 20 s context deadline — a pathological document
  becomes a clean per-item error, not a hang.
- Go's `encoding/xml` does not expand external entities (no XXE class);
  `x/net/html` caps element nesting at 512. CI gains a `govulncheck` step to
  catch future stdlib/x-net parser CVEs.
- `User-Agent: pkms/<version> (+https://github.com/rdegges/pkms)`. No
  cookies, no auth headers, TLS verification always on (no insecure flag).

## 22. RSS ingester (`type = "rss"`)

- Fetch under §21 (feed limits), parse with gofeed. Config: `url` (req).
- **NaturalKey per item:** `GUID` if non-empty; else canonical item link
  (§20 normalization); else `sha256(feedURL + "\x00" + title + "\x00" +
  published)`.
- Record fields: `title` (item title; fallback `(untitled)`), `source`
  (item link verbatim; fallback feed URL), `created` (published date →
  RFC3339; `PublishedParsed` nil → fetch time), `author` (when present),
  `tags: [clip, rss]`. Body: item content (fallback description) HTML →
  markdown via the same converter; empty → stub body noting the feed left
  no content.
- **Cursor** `{etag, last_modified}` (cursor_schema `rss/1`): conditional
  GET with `If-None-Match`/`If-Modified-Since`; `304 Not Modified` → clean
  no-op run. Dedup is complete without the cursor — it is purely a
  bandwidth courtesy.
- First run backfills every item currently in the feed (≤ 500 cap).

## 23. IMAP ingester (`type = "imap"`)

- `go-imap/v2` `imapclient`, implicit TLS (`DialTLS`, port 993). v1 ships
  no STARTTLS and no plaintext dial.
- **Read-only invariant:** mailbox opened with EXAMINE; all body fetches use
  `Peek` (never sets `\Seen`); pkms never stores flags, never deletes,
  never expunges.
- **Cursor** `{uidvalidity, last_uid}` (cursor_schema `imap/1`), resume per
  RFC 9051 / RFC 4549 §3.2:
  1. EXAMINE → read `UIDVALIDITY`, `UIDNEXT`.
  2. `uidvalidity` mismatch → log `cursor reset (uidvalidity changed)`,
     set `last_uid = 0`, full pass (dedup makes it a cheap no-op sweep).
  3. `low = last_uid + 1`; if `low >= UIDNEXT` → nothing new, skip the
     fetch entirely. This sidesteps the `X:*` gotcha: when X exceeds every
     existing UID, servers return the LAST message, not an empty set.
  4. Otherwise `UID FETCH low:*` (never `low:UIDNEXT` — misses arrivals
     racing the EXAMINE), batched, ≤ `batch` (default 200) messages per run.
  5. After the run, `last_uid = max(UID actually fetched)` — never the
     EXAMINE-time UIDNEXT.
- **NaturalKey:** `Message-ID` with angle brackets stripped and surrounding
  whitespace trimmed, compared case-sensitively (RFC 5322). Missing, empty,
  no `@`, or > 998 bytes → fallback key
  `sha256("pkms-mail\x00" + lower(trim(From)) + "\x00" + raw Date header +
  "\x00" + Subject + "\x00" + To + "\x00" + first 2 KiB of raw body)` —
  Date+From+Subject breaks Message-ID reuse (cyrus guidance); the body
  prefix breaks templated-bulk collisions (ePADD finding). `X-GM-MSGID` is
  not used (Gmail-only; keys must be uniform across providers).
- **MIME → record** (`go-message`, charset auto-decode wired to
  `x/net/html/charset`): prefer the `text/html` part (→ readability?
  no — emails are already "content"; straight html-to-markdown), else
  `text/plain` verbatim; multipart walk capped at depth 10 and 10 MiB per
  part; unknown charset is non-fatal (best-effort decode, note the fact in
  the body). Attachments are NOT stored in phase 2 — the body ends with an
  `## Attachments` list naming them (name, type, size) so nothing is
  silently dropped; storage lands with the 2.5 asset policy.
- Record fields: `title` (RFC 2047–decoded Subject; empty → `(no subject)`),
  `source` (`mid:<message-id>` — RFC 2392 scheme; question-round decision 4),
  `created` (Date header → RFC3339; unparseable → INTERNALDATE), `from`,
  `to` (string lists), `tags: [clip, email]`.
- Hostile-input posture: email is the normal-hostile case (§14) — all
  parsing under byte caps, header values sanitized for YAML injection by
  the writer's marshaller (never raw string concatenation), HTML parts run
  through the same hardened conversion as web pages.

## 24. Secrets resolution

Never in config (§3). For source `<type>:<name>` in vault `<vault>`, secret
kind ∈ {`password`, `oauth-client-id`, `oauth-client-secret`,
`oauth-refresh-token`}, resolution order:

1. **OS keyring** (go-keyring): service `pkms`, account
   `<vault>/<type>:<name>/<kind>`.
2. **Env:** `PKMS_<VAULT>_<NAME>_<KIND>` (uppercased, `-` → `_`; e.g.
   `PKMS_PERSONAL_FASTMAIL_PASSWORD`).
3. **`password_cmd`** (argv array, direct exec, stdout's first line;
   `password` kind only).
4. Nothing found → exit 2; the copy names the exact keyring account and the
   env var it looked for and shows the `pkms secret set` invocation.

Keyring failures on headless systems (no D-Bus/Secret Service) are treated
as "not found" and fall through to env/`password_cmd` — go-keyring errors
there rather than degrading on its own (dep-scout).

**`pkms secret set <source> <kind>`** prompts on stderr (no echo), stores
via go-keyring, prints the account it wrote. `rm` deletes. This is a thin
wrapper so users never fight `security`/`secret-tool` syntax.

**XOAUTH2 (Gmail BYO client):**

- `pkms auth <source>` runs the one-time interactive flow: loopback
  redirect on `127.0.0.1:<random port>` (Google's desktop-app flow; the
  device-code flow does not allow the `https://mail.google.com/` scope),
  scope exactly `https://mail.google.com/`, then stores the refresh token
  (+ client id/secret if entered interactively) in the keyring.
- Every ingest run exchanges the refresh token for a fresh access token,
  then authenticates with the XOAUTH2 SASL string
  (`user=<u>\x01auth=Bearer <tok>\x01\x01`). go-sasl ships no XOAUTH2
  mechanism (only OAUTHBEARER) — pkms implements the ~15-line
  `sasl.Client` itself.
- The OAuth setup doc must state the trap honestly: a consent screen left
  in **Testing** mode expires refresh tokens after **7 days** — the guide
  walks through publishing the app (personal use; unverified is fine for
  your own account) and flags Fastmail/self-hosted app passwords as the
  zero-OAuth alternative. Personal Gmail with 2SV also still issues app
  passwords (2026) — documented as the simpler path.

## 25. Dependency pins (phase 2 additions, verified 2026-08-03)

| module | version | role |
| --- | --- | --- |
| github.com/emersion/go-imap/v2 | v2.0.0-beta.8 | IMAP client (no stable v2 yet; pin exact) |
| github.com/emersion/go-sasl | v0.0.0-20241020182733-b788ff22d5a6 | SASL (no tags exist; pinned commit) |
| github.com/emersion/go-message | v0.18.2 | MIME parsing (RFC 2047, charset decode) |
| codeberg.org/readeck/go-readability/v2 | v2.1.2 | readability (go-shiori original **archived 2025-12-30**) |
| github.com/JohannesKaufmann/html-to-markdown/v2 | v2.5.2 | HTML → markdown (input must be UTF-8 first) |
| github.com/mmcdole/gofeed | v1.4.0 | RSS/Atom/JSON feeds (`*Parsed` dates are nil-able) |
| github.com/zalando/go-keyring | v0.2.8 | secrets (already pinned §13) |
| golang.org/x/net | v0.57.0 | html/charset decoding |
| golang.org/x/term | v0.45.0 | no-echo secret prompt (`pkms secret set`) |

Implementation notes that bind: go-imap commands are pipelined — every call
needs `.Wait()`/`.Collect()`; readability v2 exposes accessor methods +
`RenderHTML`/`RenderText` (not v1's public fields) and requires a base URL;
html-to-markdown does no charset handling and no sanitization;
`keyring.MockInit()` in unit tests, never the real keychain.

## 26. Doctor & profile additions

- `doctor` gains per-source checks: state file parses (header version
  supported, cursor schema known), quarantine count per source (> 0 →
  warning finding naming the directory), stale locks.
- Keyring reachability is checked ONLY when an ingester needs it, as a
  warning (headless machines must not fail doctor).
- `rdegges` profile: `raw-clip` gains placement templates —
  `folder = "Resources/Clips/Inbox"`,
  `filename = "{{tsname .created}} - {{.title}}"` where `tsname` renders
  RFC3339 → `2006-01-02T150405-0700` (matches `clip-processed-filename`)
  and the writer sanitizes filename-unsafe chars (`/ \ # | [ ] ^ :` → `-`)
  before collision handling. `created` carries full RFC3339 for ingested
  notes (the schema's `^\d{4}-\d{2}-\d{2}` prefix pattern already admits
  it). The raw-clip `source` pattern widens to `^(https?://|mid:|file://)`
  (question-round decision 4).
- `para` profile (question-round decision 1): scaffold gains a top-level
  `Inbox/` folder; a new minimal `clip` type targets it —
  `folder = "Inbox"`, `filename = "{{tsname .created}} - {{.title}}"`,
  schema requiring `title` (string), `source`
  (`^(https?://|mid:|file://)`), `created` (ISO prefix), `tags` (list
  containing `clip`); `additionalProperties: true` as everywhere. All
  ingested notes land in `Inbox/` for later sorting. `pkms init` on an
  existing para vault fills the missing `Inbox/` folder on next run
  (init is idempotent, §11).

## 27. Testing & acceptance additions

- Unit: state-store crash matrix (kill before rename / between rename and
  ack / after ack), ack-repair via vault source-id set, cursor persistence
  on clean vs failed Fetch, SSRF guard table-driven (every deny range + the
  v4-in-v6 unmaps), MIME dispatch table, canonical-URL + Message-ID +
  fallback-key normalization, secrets resolution order (keyring mocked).
- Hermetic tests (blind, cross-model) on the spec-able core: dedup keys,
  cursor semantics, MIME dispatch, SSRF decisions, quarantine ordering.
- e2e (txtar, extending `e2e/testdata/`): URL ingest happy path (local
  httptest fixture), re-run dedup no-op, quarantine on schema failure, SSRF
  refusal copy, `ingest` with no config, crash-recovery simulation, RSS
  fixture run-twice, IMAP against an in-process test server.
- Acceptance (local, never CI): real HTML pages + a scratch IMAP mailbox
  against a COPY of the real vault; every ingester runs TWICE and the vault
  diffs byte-identical on the second run.
- Binding phase exit (plan): scheduled email ingest runs a week with zero
  duplicate notes — cron setup happens only after explicit go-ahead, on the
  real vault, after merge.

## 28. Phase-2 amendments (post-review, 2026-08-03)

Documented deviations found during the multi-lens review (red-team +
security lens + codex). Frozen docs get amendments, never silent drift.

1. **Source-lock co-location.** §2 lists locks under `locks/<vault>[.<source>]`.
   Per-source ingest locks instead live beside their state file at
   `state/<vault>/<source>.ndjson.lock` so the lock and its ledger share a
   directory and lifetime; the per-vault lock stays at `locks/<vault>.lock`.
   flocks die with the process, so a stale source-lock FILE is always
   immediately re-lockable.
2. **Pull-mode output.** §17's `--json` shape is per-vault; the multi-vault
   cron sweep emits a JSON **array** of `{"vault","sources":[…]}` (one entry
   per vault), and push mode emits a single such object. Human pull output
   prefixes each line with the vault name (`<vault>: <source>: N new …`) to
   disambiguate the all-vaults run; the `Result.Summary()` string itself is
   the frozen `<source>: N new, M deduped, Q quarantined`.
3. **Ingester-type validation timing.** Unknown `type` is rejected at run
   time (the registry is populated by package `init()`s the config package
   can't import without a cycle), not at config load. Config load still
   validates the table shape, names, and uniqueness.
4. **One bad source never blocks the sweep.** A source whose run errors
   (e.g. a missing credential) is reported to stderr and skipped; remaining
   sources and vaults still run; the command exits 2. An explicitly targeted
   `--source`, or a single-vault run, still fails loudly and immediately.
   Mirrors §9's "push failure is a warning, never blocks".
5. **MIME-nesting guard.** §23's "multipart walk capped at depth 10" is
   enforced as (a) a raw-bytes ceiling of 100 `multipart/` headers checked
   BEFORE go-message descends the tree (deep nesting is quadratic inside a
   single `NextPart`, so a post-parse depth counter can't help), and (b) a
   flat 100-part processing cap in the walker. An over-nested message is
   recorded from its headers with a skip note in the body — acked, never
   re-fetched, never a hang.
6. **IMAP whole-message cap.** Each raw message is streamed under a 25 MiB
   ceiling (not buffered whole); an oversized message is skipped and its UID
   still advances the cursor. Complements §21's per-part 10 MiB cap.
7. **Frontmatter round-trip is enforced at write time.** §6 declares
   round-trip a test invariant; the writer now also checks it at runtime and
   quarantines any record whose marshaled frontmatter doesn't reparse to the
   same fields (a hostile title like `? x` that the YAML emitter renders as a
   plain scalar reparsing to zero fields). Prevents silently-corrupt notes.
8. **Filename length + emptiness.** Rendered basenames are capped at 180
   bytes (room for the ` N.md` collision suffix under the 255-byte FS limit)
   and a title that sanitizes to empty is quarantined — a pathological title
   truncates or quarantines, never wedges the batch (§17 step 5e).
9. **Attachment-name neutralization.** Beyond traversal, the IMAP attachment
   listing strips wikilink/embed/code/table markup (`[ ] ` `` ` `` `|`, leading
   `!`) so a sender can't smuggle `![[secret.png]]` into a note body.
10. **RSS cap + conditional GET.** When the per-run item cap drops items, the
    ETag/Last-Modified cursor is NOT advanced, so the next run re-fetches
    instead of 304-ing and starving the overflow.
11. **Cursor-schema header.** The state header carries `cursor_schema`
    (`imap/1`, `rss/1`; empty for cursor-less sources); doctor rejects an
    unknown schema (§26). RSS's no-GUID/no-link fallback key is the bare
    `sha256(feedURL\x00title\x00published)` hex (§22), no prefix.
12. **SSRF: IPv4-compatible IPv6.** The deny check also unwraps the
    deprecated `::/96` IPv4-compatible form and re-checks the embedded v4,
    alongside the v4-mapped/NAT64/6to4 handling already specified in §21.
13. **Per-ingester `note_type` override.** Every ingester config accepts an
    optional `note_type` key (SPEC §18) that overrides the profile's
    `[ingest] clip` default for that source — useful when one vault files
    feeds and email into different note types. Config `note_type` wins; the
    profile default fills in otherwise.
14. **`--json` extra fields.** The per-source object (§17) also carries
    `dropped` (per-run cap overflow) and, for push, `existing` (the note a
    deduped record already lives in). Additive to the §17 shape.
15. **Cross-model test lens.** §27 names "hermetic tests (blind,
    cross-model)". For phase 2 this was satisfied by scoped Codex
    challenge/verify passes over every spec-able core (dedup keys, cursor
    semantics, MIME dispatch, SSRF decisions, quarantine ordering, the
    durability invariants) plus two independent fresh-context review agents
    (red-team + security lens); their findings were triaged into §28. A
    fully-blind hermetic cycle (Codex authors tests from the frozen spec
    without seeing the impl) was not run for phase 2.
16. **Feed decode bound.** §21 names "feed decode" among the parse calls
    under the deadline. `gofeed.Parse` runs without an explicit timeout: it
    is a streaming pull parser fed a reader already capped at 15 MiB, and
    the measured worst case (100k items in a 10 MB feed) is sub-second. The
    byte cap is the accepted control here; `ConvertHTML` (per-item HTML→md)
    still carries the timeout.
17. **Network-source e2e.** §27's URL/RSS/IMAP happy-path e2e scripts can't
    run as specced: the SSRF guard (correctly) refuses the loopback address
    a local httptest fixture binds, and pkms ships no test-only bypass (that
    would be a production security hole). Those paths are covered by unit
    tests with injected transports/connections (`fetch`, `ingest/adhoc`,
    `ingest/rss`, `ingest/imap`) plus the local-file push e2e, which
    exercises the full fetch→convert→write→dedup→commit pipeline through the
    binary. Crash-recovery and run-twice idempotence are unit-tested in
    `internal/ingest` (the pipeline crash matrix and rerun no-op tests).
