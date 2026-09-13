# Local vault status

`pkms status` shows one vault's capture backlog, local Git recovery point,
quarantine files, and actionable inspection findings. Use `--vault <name>`
when several vaults are configured. `--json` returns the same observations
as structured data.

- **Inbox:** markdown notes under the profile's literal ingest clip/asset
  folders, counted once when folders overlap. Processed notes outside those
  folders are excluded. `oldest_created_at` is the earliest parseable
  frontmatter `created` date. Email and feed dates often describe the source,
  so this is not the time a note entered the inbox. Missing or invalid dates
  contribute to `undated_count`. Missing or templated folder declarations
  give an explicit unknown count. An absent directory counts as empty; a
  declared folder or ancestor replaced by a file or symlink fails inspection.
- **Ingest:** `last_success_at` is null. The current ingester does not keep
  durable run outcomes. State-file modification times, ledger timestamps,
  and note dates cannot prove the latest run succeeded.
- **Snapshot:** the latest local Git commit, its committer timestamp,
  unsnapshotted changes, and an in-progress merge/rebase/cherry-pick. A commit
  is a local recovery point; it does not prove that a scheduled snapshot ran
  or that a remote backup succeeded. `last_run_at` is null because successful
  clean snapshot runs leave no commit or durable run record. `dirty` is null
  when Git clean/process filters or submodules make working-tree inspection
  unsafe; the detail explains why. Partial-clone/promisor repositories are
  not inspected. HEAD must resolve to a commit with a valid timestamp;
  corrupt references and missing objects are inspection failures.
- **Quarantine:** regular files below this vault's quarantine directory,
  including adhoc and formerly configured sources. Missing directories count
  as zero; unreadable or invalid paths produce an inspection error and null
  count. Symlinks and special files are not followed or opened; their presence
  makes the count unknown and calls for review. Existing ancestors within
  the pkms state directory must also be real directories; a dangling symlink
  does not count as an absent, empty quarantine.

The command reads local files and Git state without changing the vault,
creating state files, acquiring ingest locks, contacting services, or probing
secrets. Its observations are a point-in-time view, not an atomic snapshot of
concurrent work. It validates lint configuration and enabled source types
without constructing ingesters or checking credentials. It does not run content
lint. Git transport, signature helpers, pagers, filesystem monitors, and unsafe
working-tree inspections are disabled or avoided.
Inherited `GIT_*` environment settings are discarded for inspection, and Git
Trace2 output targets are disabled, including targets from configuration files.

Exit codes: **0** means inspection completed with no known actionable issue;
explicitly unavailable run timestamps, unsupported folder templates, or unsafe
working-tree inspections remain unknown. **1** means quarantine files, missing
Git recovery setup, unavailable Git, partial-clone/promisor repositories,
unsupported quarantine entries, or an in-progress Git operation needs attention.
**2** means usage, configuration, inspection, or output failed. Dirty files alone are normal pending
snapshot work and do not change the exit code. Once the vault is selected,
subsystem errors still produce a complete JSON report with null unavailable
values and failed checks; failures take precedence over exit 1 findings.
