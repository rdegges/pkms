# pkms Lint Rule Catalog (frozen with SPEC.md)

Normative semantics for every lint rule pkms ships. Extracted 2026-08-03 from a
real production vault's written conventions and validated against its 989 notes
("reference vault" below). Examples are synthetic — no real note content.

Every rule is a pure function over (file path, parsed YAML frontmatter, markdown
AST, and — for drift rules — the vault-wide index). No LLM judgment anywhere.

**Engine vs profile:** rules are generic and parametrized; the profile supplies
the vault-specific configuration (scope globs, regexes, schemas, allowlists,
index definitions). The `rdegges` built-in profile instantiates everything
below; the generic `para` profile instantiates only the profile-agnostic subset
(frontmatter well-formedness, link rules, junk files, template placeholders,
empty notes).

Conventions:

- **Severity**: `error` = documented hard rule the reference vault upholds;
  `warning` = documented soft rule, or a hard rule currently violated at scale
  (an error would drown the first report). Severity is per-rule profile config.
- **Fixable**: `--fix` performs only idempotent, unambiguous repairs. Fix twice
  = second run is a no-op (test invariant).
- **Note-type detection is deterministic by path** (see the type map at the
  end), with one content trigger (clip-summary).
- `HHMM` in filename regexes = `([01][0-9]|2[0-3])[0-5][0-9]`.
- **Malformed config fails the run.** A glob or regex pattern that cannot
  compile (junk-file patterns, scope/lists/counts globs), a `warning_types`
  entry that names no declared note type, or a list-valued option written as
  a bare scalar or containing a non-string entry, stops `pkms lint` with
  exit 2 before any findings are reported or fixes applied. `--fix`
  validates through the same path. Config validation runs only for the
  rules a run instantiates: rules disabled with `enabled = false`, or
  excluded by `--rules`, are not validated. Profile type-scope globs are
  validated when the profile loads.

---

## Group A — Frontmatter / schema (19 rules)

### frontmatter-present
- Severity: error. Scope: all typed notes (person, meeting, daily-brief,
  project, clip-summary, recipe, raw-clip). Session traces are covered by
  `session-trace-schema` instead (warning — see divergences).
- Check: line 1 is exactly `---` and a closing `---` line exists before body
  content.
- Pass: a person note opening `---\nlast_met: 2026-07-15\n…`.
- Fail: a person note starting directly with `# Jane Doe` (reference vault: 5
  People notes + 3 Project notes fail today).
- Fixable: no (values not derivable).

### frontmatter-closed
- Severity: error. Scope: any file whose first line is `---`.
- Check: a second `---` fence exists; the block between parses as YAML.
- Pass: any well-formed note.
- Fail: `---\ndate: 2026-05-06\n# Title` (no closing fence).
- Fixable: **yes** — insert `---` before the first non-YAML-parseable line.

### frontmatter-valid-yaml
- Severity: error. Scope: all frontmatter blocks.
- Check: block parses as a YAML **mapping** (not scalar/sequence).
- Pass: any sampled note. Fail: `description: "unterminated`.
- Fixable: only via `frontmatter-no-tabs` sub-case; otherwise report-only.

### frontmatter-no-tabs
- Severity: error. Scope: all frontmatter blocks.
- Check: no `\t` inside the block.
- Pass: spaces-only indentation. Fail: `topics:\n\t- ai-security`.
- Fixable: **yes** — each leading tab → two spaces.

### date-format-iso
- Severity: error. Scope: frontmatter keys with date semantics (`date`,
  `last_met`, `created`, `updated`, `last_updated`, `date_clipped`,
  `date_published`, `published`; profile-configurable list).
- Check: value matches `^\d{4}-\d{2}-\d{2}$` AND is a real calendar date.
  Empty and `null` values fail. A full RFC3339 timestamp also passes
  (amended in SPEC §31.10: the ingest pipeline stamps `created` as RFC3339
  with offset per SPEC §20, and a valid timestamp is not a malformed date).
- Pass: `last_met: 2026-07-15`. Fail: `last_met:` (empty), `last_met: null`,
  `date: 2026-13-01`.
- Fixable: only unambiguous re-formats (`2026/07/15`, `July 15, 2026` →
  `2026-07-15`). Ambiguous (`05/07/2026`) and empty/null report-only.

### person-required-keys
- Severity: error (warning on profiles that list person in
  `frontmatter-schema.warning_types` — rdegges does). Scope: person notes.
- Check: `last_met` (ISO date), `meeting_count` (integer ≥ 0), `topics` (list
  of strings) all present. No `type` key is required (none documented, none
  observed).
- Pass: all three present and typed. Fail: a person note with no frontmatter
  (~6/378 in the reference vault).
- Fixable: no. (`last_met`/`meeting_count` are in principle derivable from the
  `## Meeting History` body section, but that section can itself be stale —
  deriving would encode drift.)

### person-topics-kebab-case
- Severity: warning. Scope: person `topics` entries.
- Check: each entry matches `^[a-z0-9]+(-[a-z0-9]+)*$`.
- Pass: `- ai-security`. Fail: `- AI Security` / `- ai_security`.
- Fixable: **yes** — lowercase; spaces/underscores → `-`.

### meeting-required-keys
- Severity: error (warning on profiles that list meeting in
  `frontmatter-schema.warning_types` — rdegges does). Scope: meeting notes
  (`HHMM - *.md`; excludes `daily-brief.md` / `pre-brief.md`).
- Check: `date` (ISO), `time` (`"HH:MM - HH:MM"`), `duration` (integer
  minutes), `type: meeting`, `has_transcript` (boolean), `attendees` (list of
  `"[[Full Name]]"` strings), `tags` (list containing `meeting` + the
  lowercase domain segment from the path). Extra keys (e.g. `recording`)
  allowed.
- Pass: all keys present. Fail: missing `has_transcript`/`duration` (2 real
  hits in the reference vault).
- Fixable: only the path-derivable `tags` domain entry.

### meeting-attendees-are-wikilinks
- Severity: error. Scope: meeting `attendees` entries.
- Check: each entry matches `^\[\[[^\]|#]+\]\]$` after YAML unquoting (no
  aliases, no fragments).
- Pass: `- "[[Jane Doe]]"`. Fail: `- Jane Doe`, `- "[[Jane Doe|Jane]]"`.
- Fixable: **yes** — wrap a bare name in `[[…]]`.

### meeting-date-matches-path
- Severity: error. Scope: notes under `Meetings/*/YYYY/MM/DD/`.
- Check: frontmatter `date` equals the date implied by the directory path.
- Fixable: no (cannot know whether path or frontmatter is wrong).

### meeting-time-matches-filename
- Severity: warning. Scope: `HHMM - *.md` notes with a `time` key.
- Check: filename `HHMM` == start of `time` with the colon removed.
- Pass: `1100 - Weekly Sync.md` + `time: "11:00 - 12:00"`. Fail: same file
  with `time: "13:00 - 14:00"`.
- Fixable: no (ambiguous which side is authoritative).

### daily-brief-schema
- Severity: error. Scope: `daily-brief.md` under meeting date dirs.
- Check: `date` (ISO, == path date), `type: daily-brief`, `tags` list
  containing `daily-brief` + domain. Inline YAML list form is valid.
- Fixable: `type: daily-brief` derivable from filename → yes for that key.

### project-required-keys
- Severity: warning (16/107 reference projects fail today; template is loose).
- Scope: project notes.
- Check: `type: project`, `category` (matching the domain enum), `status`
  (string), `created` (ISO), `updated` (ISO), `description` (string).
- Fixable: `type: project` + `category` path-derivable → yes for those two.

### project-category-matches-path
- Severity: error. Scope: project notes that have `category`.
- Check: `category` equals the parent folder name.
- Fixable: **yes** — path is the documented source of truth; fix rewrites
  `category` to match it.

### project-status-vocab
- Severity: warning. Scope: project notes with `status`.
- Check: `status` ∈ configurable allowlist. Default (from observed usage):
  `active`, `inactive`, `idea`, `proposed`, `in-progress`, `archived`.
- Pass: `status: active`. Fail: a free-text one-off status.
- Fixable: no.

### resource-clip-schema
- Severity: error. Scope: **trigger-scoped**: Resources notes whose
  frontmatter contains `source_url` OR `date_clipped`. (Only ~43/115
  reference Resources pages are clips; applying this to all of Resources/
  would be ~60% false positives.)
- Check: `source_url` (http/https URL), `date_clipped` (ISO), `topics` (list),
  `related_people` (list of wikilink strings, may be empty),
  `related_projects` (same), `tags` (list) — all present and typed.
- Fixable: no.

### recipe-schema
- Severity: error. Scope: recipe notes (Recipes folder, excluding the index).
- Check: `type: recipe`, `servings` (int), `course` (string), `tags` (list
  containing `recipe`). Optional keys type-checked when present:
  `*_time_min` / `calories_per_serving` integers, `dietary` list.
- Fixable: `type: recipe` derivable from path → yes for that key.

### recipe-macros-estimated-flag
- Severity: error. Scope: recipe notes with `calories_per_serving`.
- Check: `macros_estimated` present and boolean (estimated nutrition must be
  labeled — documented hard rule).
- Fixable: no (whether the number was estimated is not derivable).

### session-trace-schema
- Severity: warning (11/12 reference traces have no frontmatter at all).
- Scope: session-trace notes (excluding `_template.md`).
- Check: frontmatter with `date` (ISO), `slug` (string), `loop_levels_touched`
  (list), `tags` (list containing `session-trace`).
- Fixable: **partially** — create the block with `date` + `slug` derived from
  a conforming filename + `tags: [session-trace]`; still warns about
  non-derivable keys.

### clip-raw-schema
- Severity: warning. Scope: raw clips (`Clips/{Inbox,Processed}`).
- Check: `title`, `source` (URL), `created` (ISO), `tags` (list containing
  `clip`) — the web-clipper contract; a clip without them won't dedup.
- Fixable: no.

### frontmatter-key-order
- Severity: warning, **off by default**. Scope: typed notes with a template.
- Check: known template keys appear in template order; unknown keys allowed
  anywhere after the known prefix.
- Fixable: **yes** — stable re-order, value-preserving.

---

## Group B — Naming / placement (13 rules)

### root-canonical-only
- Severity: error. Scope: files directly in the vault root.
- Check: root files ⊆ the profile's canonical set (for `rdegges`: `Now.md`,
  `Preferences.md`, `Projects.md`, `People.md`, `Areas.md`,
  `Action Items.md`, `index.md`, `log.md`) + configurable allowlist.
- Fail examples (all real classes): `Action Items.md.bak`, `.test_write`,
  stray `.DS_Store`.
- Fixable: no (move/delete is a decision).

### root-file-name-case
- Severity: error. Scope: root `.md` files.
- Check: exact casing against the canonical set (human-facing capitalized;
  machine files exactly `index.md`, `log.md`).
- Fail: `now.md`, `Index.md`. Fixable: no (rename breaks links).

### top-level-folders-fixed
- Severity: error. Scope: directories in the vault root.
- Check: directory set ⊆ profile's top-level set + allowlist (e.g. an
  attachments folder like `+/` must be explicitly allowlisted).
- Fixable: no.

### domain-split-folders
- Severity: error. Scope: first-level subdirs of top-level folders.
- Check: per-folder allowed-subdir sets from the profile (for `rdegges`:
  `Projects|Archive|Meetings|People` → {Snyk, Personal}; `Areas` → {Snyk,
  Personal, Writing}; `Resources` → {Snyk, Personal, Clips}). Loose files
  directly under a top-level folder also fail.
- Fixable: no.

### meeting-filename-format
- Severity: error. Scope: `.md` under meeting date dirs.
- Check: basename matches `^HHMM - .+\.md$` OR is exactly `daily-brief.md` /
  `pre-brief.md`. (237/237 reference meeting notes pass.)
- Fail: `Meeting with Sam.md`, `2515 - Standup.md`.
- Fixable: no.

### meeting-path-valid-date
- Severity: error. Scope: dirs under `Meetings/<domain>/`.
- Check: structure exactly `YYYY/MM/DD`, zero-padded, real date; no files at
  intermediate levels.
- Fail: `2026/5/6/`, `2026-05-06/`. Fixable: no.

### clip-processed-filename
- Severity: warning. Scope: raw clips.
- Check: basename matches `^\d{4}-\d{2}-\d{2}T\d{6}[+-]\d{4} - .+\.md$`.
- Fixable: no.

### session-trace-filename
- Severity: error. Scope: session-trace files.
- Check: basename matches `^\d{4}-\d{2}-\d{2} — .+\.md$` (em dash) or is
  `_template.md`.
- Fail: `2026-07-10 - my-trace.md` (hyphen).
- Fixable: hyphen→em-dash sub-case only, and only when the target has no
  inbound links (or all inbound links are rewritten in the same fix).

### filename-safe-chars
- Severity: error. Scope: all `.md` files.
- Check: basename contains none of `/ \ # | [ ] ^ :` and no control chars.
- Fail: `AI: The Future #1.md`. Fixable: no.

### no-per-run-notes
- Severity: error. Scope: all `.md` files.
- Check: basename does not match banned per-run patterns (case-insensitive):
  `(Email|Slack|Meeting) Sync\b`, `Proactive (Prep|Run|Work)\b`,
  `Vault Maintenance( Report)?\b`, `Email Triage\b`, `OOO .*Sweep`,
  `Vacation Catch-?up`, `^(Sync|Run) \d{4}-\d{2}-\d{2}`.
- Pass: `daily-brief.md` (sanctioned per-day artifact).
- Fail: `Slack Sync 2026-05-17.md` anywhere. Fixable: no.

### no-drafts-folder
- Severity: error. Scope: vault-wide.
- Check: no `Drafts/` directory; no basename matching
  `^(Slack|Email) - .+ - .+\.md$` (message-draft pattern).
- Fixable: no.

### no-junk-files
- Severity: warning. Scope: vault-wide, excluding `.obsidian/` and the
  allowlisted attachments folder.
- Check: no `*.bak`, `.DS_Store`, `* (Conflicted copy*` (Obsidian Sync's
  capitalized artifact name), `* conflicted copy*`, `~$*`, `*.tmp`, and no
  leading-underscore scratch files outside sanctioned templates
  (`_template.md` allowed). (Reference vault: 17 `.bak` + 3 `.DS_Store`
  today.)
- Fixable: no (deletion is never an auto-fix).

### non-markdown-in-note-folders
- Severity: warning. Scope: `People/**`, `Meetings/**` only (Resources and
  Projects deliberately colocate assets).
- Check: only `.md` files allowed in scope.
- Fixable: no.

### template-placeholders-in-real-notes
- Severity: error. Scope: all `.md` except `_template.md` / `*_template.md`.
- Check: no `{{…}}` placeholders in frontmatter or body.
- Fixable: `{{date}}`/`{{slug}}` derivable from a conforming filename → yes
  for those two, else no.

### empty-note
- Severity: warning. Scope: all `.md` files.
- Check: at least one non-whitespace character outside the frontmatter block.
- Fixable: no.

---

## Group C — Links (7 rules)

### wikilink-resolves
- Severity: error (heading/block-anchor sub-check: warning). Scope: all
  wikilinks in body + frontmatter, excluding code fences and inline code
  (AST-aware).
- Check: resolution per SPEC §5 (path match, else basename, else alias;
  case-insensitive, NFC-normalized). Zero matches = broken.
- Fail example: `[[Nonexistent Person]]` in a meeting's attendees when no
  such note exists anywhere.
- Fixable: single-repair only — exactly one existing basename within
  **Levenshtein ≤ 1** (case-insensitive matches already resolve, per §5).
  Otherwise report. (Amended from ≤ 2 during acceptance: at distance 2 the
  repair guesses between real distinct names — observed `[[Jamie Cairns]]`
  "repaired" to the different real person James Cairns.) Targets or
  candidates containing `[ ] | #` are never auto-repaired (cannot
  round-trip through wikilink syntax).

### wikilink-ambiguous
- Severity: warning. Scope: bare-basename wikilinks.
- Check: target basename matches ≥ 2 files (duplicate basenames make bare
  links ambiguous). Companion rule `duplicate-basename` fires on the files.
- Fixable: report-only in practice.

### attendee-links-resolve-to-people
- Severity: error. Scope: meeting `attendees` entries.
- Check: every attendee wikilink resolves to a file under `People/`
  specifically.
- Fixable: no (creating a person note needs content — agent-layer work).

### related-people-resolve-to-people
- Severity: error. Scope: `related_people` lists in Resources notes.
- Check: every entry resolves under `People/`. Note: external content
  authors deliberately get no People file, so linking one here fails —
  correctly, per the documented external-author rule.
- Fixable: no.

### related-projects-resolve
- Severity: warning. Scope: `related_projects` (Resources) and `related`
  (projects) lists.
- Check: every entry resolves under `Projects/` **or `Archive/`** (projects
  get archived; such links are legal).
- Fixable: single-repair as `wikilink-resolves`.

### person-meeting-history-links-resolve
- Severity: warning. Scope: `## Meeting History` bullets in person notes
  (pattern `- **YYYY-MM-DD** — [[HHMM - Title]] — …`).
- Check: the link resolves to a meeting note whose path date equals the
  bullet's bold date.
- Fixable: path-qualify when exactly one matching file exists on that date.

### no-broken-embed
- Severity: error. Scope: `![[…]]` embeds.
- Check: embed target resolves (targets may be any vault file, incl. images).
- Fixable: single-repair as `wikilink-resolves`.

### orphan-notes
- Severity: warning. Scope: top-level notes under `Projects/<domain>/` and
  `Resources/<domain>/`.
- Check: ≥ 1 inbound wikilink from anywhere OR listed in the profile-declared
  master catalog (Projects.md / index.md / Recipes.md). Exclusions (never
  orphan-checked): `Meetings/**`, `People/**`, `Resources/Clips/**`, session
  traces, `Areas/**`, `Archive/**`, root canonical files, templates, non-md.
- Fixable: no.

---

## Group D — Index / count drift (11 rules)

All parametrized by the profile's `[[indexes]]` and count-field declarations.

### action-items-count-drift
- Severity: error. Scope: `Action Items.md`.
- Check: frontmatter `total_items` equals the count of ALL task-list items
  (`- [ ]` + `- [x]`, any indent) in the body. (Semantics verified against
  the reference vault: the field counts all task items.)
- Fixable: **yes** — recompute (derived value).

### recipes-count-drift
- Severity: error. Scope: the recipe index note.
- Check: `recipe_count` equals the number of recipe `.md` files (excluding
  the index itself). (Reference vault fails today: 14 vs 13 — expected
  first-run finding.)
- Fixable: **yes** — recompute.

### recipes-index-links-complete
- Severity: error. Scope: recipe index vs recipe files.
- Check: both directions — every recipe file wikilinked at least once from
  the index; every recipe wikilink in the index resolves.
- Fixable: no (section placement / removal is judgment).

### resources-cataloged-in-index
- Severity: warning (documented as mandatory, but 75/115 reference pages are
  missing today — error would be pure noise).
- Scope: top-level notes under `Resources/<domain>/`.
- Check: each basename appears as a wikilink target in `index.md`.
- Fixable: no (the catalog line is authored content).

### projects-linked-from-master
- Severity: warning (~60/107 unlinked today). Scope: project notes vs
  `Projects.md`.
- Check: each project basename appears as a wikilink target in `Projects.md`.
- Fixable: no.

### index-no-inventory
- Severity: warning. Scope: `index.md`.
- Check: zero wikilinks resolving into `Meetings/**` or `People/**` (the
  index catalogs Resources; it never re-lists meetings or people).
- Fixable: no.

### log-entry-format
- Severity: error. Scope: `log.md`.
- Check: every `##` heading matches
  `^## \[\d{4}-\d{2}-\d{2}\] [a-z][a-z-]* \| .+$` with a valid date; each
  entry body has ≥ 1 `- ` bullet.
- Fixable: no.

### log-action-vocab
- Severity: warning. Scope: the action token in each `log.md` H2.
- Check: action ∈ configurable allowlist. `rdegges` default (documented five
  + observed high-frequency): `ingest`, `lint`, `update`, `create`,
  `archive`, `slack-sync`, `email-sync`, `proactive-work`, `daily-brief`,
  `maintenance` (~97% coverage of the reference log).
- Fixable: no.

### log-newest-first
- Severity: error. Scope: `log.md` H2 sequence.
- Check: entry dates non-increasing top to bottom.
- Fixable: no (duplicate dates make reordering risky).

### now-line-cap
- Severity: warning at 60 lines, error at 80 (configurable pair; reference
  Now.md is 74 lines today — expected finding).
- Scope: `Now.md`. Check: total line count against thresholds.
- Fixable: no.

### now-fixed-sections
- Severity: error. Scope: `Now.md` H2 headings.
- Check: H2s, in order, prefix-match exactly: `Today`, `This Week`,
  `Active Projects`, `Blocked / Waiting On`, `Watching`, `Notes` (suffix
  annotations allowed, e.g. `## This Week (Mon 8/3 →)`). No other H2s.
- Fixable: no.

*(companions to Now.md, still Group D config)*

### now-no-sync-sections
- Severity: error. Scope: all headings in `Now.md`.
- Check: no heading matches (case-insensitive) `New from .*`,
  `.*[Ss]ync.*\d{4}-\d{2}-\d{2}`, `Proactive Prep.*`,
  `From .* (email|slack) sync`.
- Fixable: no (content must be relocated, not deleted).

### now-active-projects-shape
- Severity: warning. Scope: the `## Active Projects` section of `Now.md`.
- Check: 5–8 top-level bullets, zero nested bullets.
- Fixable: no.

---

## Group E — Structural body rules (6 rules)

### person-required-sections
- Severity: warning (13/378 reference notes lack `## Meeting History`).
- Scope: person notes.
- Check: body contains H2s `## Meta`, `## Relationship`, `## Meeting History`
  (extras allowed anywhere).
- Fixable: **yes** — insert the missing heading, empty, in template position.

### person-meta-last-updated
- Severity: warning. Scope: `## Meta` section of person notes.
- Check: a bullet matching `^- \*\*Last Updated\*\*: \d{4}-\d{2}-\d{2}$`.
- Fixable: date-format normalization only; missing bullet no.

### person-meeting-history-bullet-format
- Severity: warning. Scope: `## Meeting History` bullets.
- Check: each top-level bullet matches
  `^- \*\*\d{4}-\d{2}-\d{2}\*\* — \[\[.+\]\] — .+$`.
- Fixable: no.

### meeting-required-sections
- Severity: warning. Scope: meeting notes.
- Check: body contains `## Attendees`, `## Summary`, `## Key Decisions`
  (extras allowed).
- Fixable: **yes** — insert missing empty heading.

### daily-brief-per-day
- Severity: warning. Scope: meeting date dirs containing ≥ 1 `HHMM - *.md`.
- Check: the dir also contains `daily-brief.md`.
- Fixable: no (brief content comes from the daily job, not lint).

### log-entry-bullets-flat
- Severity: info/off by default (documented "flat list" conflicts with real
  practice — newest reference entries use nested lists).
- Scope: `log.md` entry bodies. Check: bullets at indent 0 only.
- Fixable: no.

---

## Group F — Raw text well-formedness (1 rule, added by SPEC §33)

### note-valid-text
- Severity: error. Scope: every note, both profiles (profile-agnostic); runs
  on the raw bytes, frontmatter included, and on over-cap (`TooLarge`) notes.
  "Every note" means exactly the files the index treats as notes —
  case-sensitive `.md` — so a corrupt `Note.MD` is outside the claim.
- Check: the file is valid UTF-8 and contains no C0 control bytes other than
  tab, LF, and CRLF pairs, and no DEL (0x7F). A bare CR fails. C1 controls
  are legal (out of scope — no false positives on odd-but-legal Unicode).
- Pass: any UTF-8 note, including CRLF line endings, tabs, emoji, CJK.
- Fail: a NUL byte, an ANSI escape (0x1B), a lone CR, latin-1 bytes (`caf\xe9`).
- Findings: at most two per note (one per defect kind), each with the count
  and first offending line — a binary file misnamed `.md` cannot flood the
  report.
- Over-cap notes: an over-`MaxBodyParseSize` note is indexed without its
  bytes, so the rule stream-scans it from disk in fixed-size chunks. One
  that cannot be read fails closed with an error finding ("could not read
  note for text validation") — size or unreadability never buys a pass.
- Fixable: no (stripping bytes is destructive; repair is the owner's call).

---

## Per-type frontmatter field tables (JSON Schema source of truth)

`req` = required; `opt` = type-checked when present. All schemas set
`additionalProperties: true`.

**person** — `last_met` ISO date (req), `meeting_count` int ≥ 0 (req),
`topics` string[] kebab-case (req). No `type` key.

**meeting** — `date` ISO == path (req), `time` `"HH:MM - HH:MM"` (req),
`duration` int minutes (req), `type` const `meeting` (req), `has_transcript`
bool (req), `attendees` wikilink-string[] (req), `tags` string[] ⊇ {`meeting`,
domain} (req), `recording` URL (opt).

**daily-brief** — `date` ISO == path (req), `type` const `daily-brief` (req),
`tags` string[] ⊇ {`daily-brief`} (req).

**project** — `type` const `project`, `category` domain enum == path,
`status` allowlisted string, `created` ISO, `updated` ISO, `description`
string (all req-at-warning); `repo`/`github`/`url` URL, `related`
wikilink-string[], `is_oss` bool, `language` string, `stars` int (opt).

**clip-summary** (trigger: `source_url` or `date_clipped` present) —
`source_url` URL (req), `date_clipped` ISO (req), `topics` string[] free-form
(req), `related_people` wikilink-string[] may-be-empty (req),
`related_projects` wikilink-string[] may-be-empty (req), `tags` string[]
(req); `type`, `source_format`, `author` strings, `date_published` ISO (opt).

**recipe** — `type` const `recipe` (req), `servings` int (req), `course`
string (req), `tags` string[] ⊇ {`recipe`} (req); `domain`, `status`,
`created`, `last_updated`, `source_url`, `source_type`, `cuisine` scalars,
`prep_time_min`/`cook_time_min`/`total_time_min`/`calories_per_serving` int,
`dietary` string[] (opt); `macros_estimated` bool required IFF
`calories_per_serving` present.

**recipe-index** — `type` const `resource`, `sub_domain` const `recipes`,
`recipe_count` int (drift-checked), `last_updated` ISO.

**session-trace** — `date` ISO (req), `slug` string (req),
`loop_levels_touched` string[] (req), `tags` string[] ⊇ {`session-trace`}
(req), `duration_estimate` (opt). Severity: warning (see rule).

**raw-clip** — `title` string, `source` URL, `created` ISO, `tags` string[] ⊇
{`clip`} (req at warning); `author` wikilink-string[], `published` ISO,
`description` string (opt).

**Root files** — `Action Items.md`: `updated` ISO, `total_items` int
(drift-checked). All other roots: no frontmatter (body rules only).

---

## Implementation mapping (catalog rule → engine rule ID)

The engine consolidates the pure-schema catalog rules into one parametrized
rule, `frontmatter-schema` (per SPEC §12: rules are generic, profiles
instantiate). Findings carry the engine rule ID; semantics are unchanged:

- `person-required-keys`, `meeting-required-keys` (schema-expressible part),
  `daily-brief-schema`, `project-required-keys`, `resource-clip-schema`,
  `recipe-schema`, `recipe-macros-estimated-flag` (via `dependentRequired`),
  `session-trace-schema`, `clip-raw-schema` → **`frontmatter-schema`**,
  validated against the profile's per-type JSON Schemas. Per-type warning
  severity comes from its `warning_types` config.
- Missing-frontmatter detection is **`frontmatter-present`** (its own
  `warning_types` covers session-trace/raw-clip; the session-trace fix
  creates the block with filename-derived `date`/`slug`).
- The path-dependent slice of `meeting-required-keys` (tags must contain the
  domain segment) is **`meeting-tags-domain`**.
- `meeting-date-matches-path` also covers the daily-brief date==path check.
- All other catalog rules keep their IDs 1:1.

**Fixes shipped report-only in v1** (catalog said fixable; deferred on the
safe side — checks are unchanged): schema-derivable required keys
(`type: project`, `category`, `type: daily-brief`, `type: recipe` — the
schema rule reports, no auto-insert), `no-broken-embed` single-repair,
`related-projects-resolve` single-repair, and `person-meta-last-updated`
date normalization. The session-trace frontmatter fix (via
`frontmatter-present`) and all other listed fixes ARE implemented.
`frontmatter-key-order` requires explicit `enabled = true` + per-type
`orders` config; its fix moves whole key line-spans verbatim.

## Deferred to the agent layer (judgment — never lint rules)

1. PARA bucket correctness (Projects vs Areas vs Resources; domain call).
2. Dedup by meaning / merge decisions.
3. External-author vs real-contact decisions (create the People file, or
   remove the link?).
4. Now.md content quality (top-3 selection, phrasing, what overflows where).
5. Meta field accuracy (placeholder/AI-inferred values, wrong emails).
6. `last_met`/`meeting_count` provenance correctness.
7. Honest-stub rule (never fabricate a synthesis of unread content).
8. log.md entry completeness (does the entry cover the run?).
9. Writing index.md one-line descriptions (lint detects the gap only).
10. Archive decisions (what counts as completed).
11. Recognizing action-items-as-notes.
12. Sanctioned-vs-junk calls for non-md assets in Resources/.
13. Trust-boundary flagging of instruction-shaped clip content.
14. Multi-candidate wikilink repair; renames for unsafe filenames.
15. Root-junk cleanup decisions (delete, move, or allowlist).

---

## Note-type map (deterministic classification, `rdegges` profile)

| path pattern | type |
|---|---|
| root `Now/Preferences/Projects/People/Areas/Action Items.md` | canonical-root |
| root `index.md`, `log.md` | machine-root |
| `People/{Snyk,Personal}/*.md` | person |
| `Meetings/*/YYYY/MM/DD/HHMM - *.md` | meeting |
| `Meetings/*/YYYY/MM/DD/daily-brief.md` | daily-brief |
| `Meetings/*/YYYY/MM/DD/pre-brief.md` | pre-brief (placement rules only) |
| `Projects/{Snyk,Personal}/*.md` (not `Blog Drafts/`) | project |
| `Resources/Personal/Recipes/Recipes.md` | recipe-index |
| `Resources/Personal/Recipes/*.md` | recipe |
| `Resources/Personal/Session Traces/*.md` | session-trace |
| `Resources/Clips/{Inbox,Processed}/*.md` | raw-clip |
| `Resources/{Snyk,Personal}/*.md` + trigger | clip-summary |
| `Resources/{Snyk,Personal}/*.md` otherwise | resource-generic (placement + links only) |
| `Areas/**/*.md` | area (placement + links only) |
| `Archive/**` | archived (links only; schemas not enforced) |
| everything else | unclassified → placement-rule territory |
