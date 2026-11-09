ADR-0074: Run retention
=======================

:Status: Accepted
:Date: 2026-11-09
:Amends: ARCHITECTURE section 8.4 (retention duty)

Issue :issue:`234`. Related: ADR-0054 (audit retention), ADR-0067 (deferred log
deletes).

Context
-------

Runs, executions and logs were kept forever. With ``logs.store = db`` the
database grows with every run and so do its backups. Only audit events could be
pruned.

Decision
--------

**Opt-in.** ``runs.retention_days`` (environment ``DAMSON_RUNS__RETENTION_DAYS``)
defaults to ``0``, which keeps everything. The maximum is 36500; a negative
value fails validation.

**What is deleted.** With a positive value, the singleton ``retention`` duty (the
same lease-fenced leader duty as audit retention, one pass an hour, on one
controller) deletes runs that are ``succeeded``, ``failed`` or ``canceled``,
whose ``finished_at`` is older than N days and that have no queued, dispatched
or running execution, oldest first. Their step instances, executions, run
targets (by cascade) and log chunks go with them. Runs that are not finished,
including stuck ones, are never pruned.

**Batches.** Up to 20 runs per transaction; a batch that exceeds the call
timeout is retried with half as many runs, down to one. A pass runs for at most
half the retention interval (30 minutes), so the rest of a large backlog waits
for the next pass. Each batch is fenced by the lease term and takes the time for
the lease check and its audit event when it starts, so a controller that lost
the lease deletes nothing. Finished runs are listed through a partial index on
``finished_at`` (migration 00024). The data of ``db`` log chunks is deleted by
key, never by prefix scan. With ``audit.retention_days = 0`` every batch adds
one ``run.prune`` event to the audit trail, which therefore grows with the
number of pruned runs.

**Logs.** Chunk rows and ``logs.store = db`` data are deleted in the batch's
transaction. Objects in external backends (S3) are queued in the
``log_object_deletes`` outbox in the same transaction and removed by the
per-controller log sweeper (ADR-0067), only for executions that had external
chunks.

**Audit.** Each batch writes one ``run.prune`` event in its transaction (target
type ``instance``, target ``runs``; details ``runs``, ``executions``,
``log_chunks``, ``log_objects``, ``queued_log_deletes``, ``before``), actor
``system:retention``. It holds counts, no run content. A pass logs ``pruned
finished runs`` at INFO with the counts.

**Not deleted.** Jobs, job versions, schedules, webhooks, secrets, audit events
(``audit.retention_days`` is separate) and agents.

Alternatives
------------

- **Per-job or per-project retention.** Deferred: it needs a policy model, API
  and UI. One global setting solves the growth problem now.
- **Table partitioning by time.** Deferred: it adds schema and migration
  complexity for both databases. Batched deletes are enough at the expected
  scale.
- **Archiving before delete.** Not done: it needs a target and format. Back up
  the database first if you need the history.

Consequences
------------

- Deletion is hard and permanent. Pruned runs return 404 in the UI and API.
- A schedule's ``last_run_id`` can point to a run that no longer exists.
- Large backlogs clear over several hourly passes; the delete load is bounded
  per pass.
- Only the lease holder prunes, so several controllers do not duplicate work.
- A per-project policy remains open.
