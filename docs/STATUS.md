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
  contribute to `undated_count`. Templated or missing capture folders give
  an explicit unknown count.
- **Ingest:** `last_success_at` is null. The current ingester does not keep
  durable run outcomes. State-file modification times, ledger timestamps,
  and note dates cannot prove the latest run succeeded.
- **Snapshot:** the latest local Git commit, its committer timestamp,
  unsnapshotted changes, and an in-progress merge/rebase/cherry-pick. A commit
  is a local recovery point; it does not prove that a scheduled snapshot ran
  or that a remote backup succeeded. `last_run_at` is null because successful
  clean snapshot runs leave no commit or durable run record.
- **Quarantine:** regular files below this vault's quarantine directory,
  including adhoc and formerly configured sources. Missing directories count
  as zero; unreadable or invalid paths produce an inspection error and null
  count. Symlinks and special files are not followed or opened; their presence
  makes the count unknown and calls for review.

The command reads local files and Git state without changing the vault,
creating state files, acquiring ingest locks, contacting services, or probing
secrets. Its observations are a point-in-time view, not an atomic snapshot of
concurrent work. It validates lint configuration but does not run content lint.

Exit codes: **0** means inspection completed with no known actionable issue;
explicitly unavailable run timestamps or unsupported folder templates remain
unknown. **1** means quarantine files, missing Git recovery setup, unavailable
Git, unsupported quarantine entries, or an in-progress Git operation needs
attention. **2** means usage,
configuration, or inspection failed. Dirty files alone are normal pending
snapshot work and do not change the exit code. Once the vault is selected,
subsystem errors still produce a complete JSON report with null unavailable
values and failed checks; failures take precedence over exit 1 findings.
