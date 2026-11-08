ADR-0067: S3 log store and deferred log object deletes
======================================================

:Status: Accepted
:Date: 2026-11-08
:Amends: :doc:`0053-audit-events` (purge semantics for external objects)

Issue :issue:`216`. Refines ARCHITECTURE section 10. Related:
:doc:`0047-agent-side-masking-only`.

Context
-------

Log chunks lived only in the database. Installations with a large log volume
need an S3-compatible object store, while the database must stay the source of
truth and purge must stay audited and atomic. An S3 delete cannot join a
database transaction.

Decision
--------

**Package.** ``internal/logblob`` defines ``Blob`` with ``Put``, ``Get`` and
``DeletePrefix``. A ``Set`` routes reads by ``log_chunks.backend`` (``db`` or
``s3``) and sends writes to the backend named by ``logs.store``. ``db`` is
always registered. ``s3`` is registered for reads whenever ``logs.s3.bucket``
is set, even with ``logs.store = db``, so switching between ``db`` and ``s3``
keeps old chunks readable as long as the ``s3`` block stays. Removing or
changing the bucket, endpoint or prefix abandons the objects written under the
old values. An unknown backend or a missing object is a gap in the SSE stream
with a warning that names the backend. ``fs`` is refused at validation
("not implemented").

**Flush.** The object ``Put`` comes first, under its own ``StoreTimeout``; the
``log_chunks`` row is inserted only after it succeeded. A ``Put`` error leaves
the lines pending and unacked, and the next flush interval retries. The key is
the same, so a retry overwrites and is idempotent. If ``InsertLogChunk`` is
then refused as stale or not found, the ingest best-effort deletes the object
it just wrote (covers a claim that committed before the first row existed).
``StoreTimeout`` of the logs component must stay below the 2 minute claim grace;
the component's config check refuses a larger value.

**Configuration.** ``logs.store`` (``db`` default, or ``s3``) and the
``logs.s3.*`` keys (:ref:`reference-config-logs`). Without ``access_key_id`` the
minio credential chain is used (AWS environment variables, then IAM or IRSA).
TLS is on by default and cannot be disabled; a plain ``http`` endpoint needs
``allow_plain_http``. With ``logs.store = s3`` start probes the bucket (10 s)
and refuses to start on failure. Warnings: ``s3`` configured but read-only
(``logs.store = db``), and a plain http endpoint. ``Redacted()`` masks
``secret_access_key`` and ``session_token``.

**Deferred deletes (schema change).** Migration 00022 adds
``log_object_deletes(id, prefix, not_before, tries, created_at)`` with an index
on ``not_before``, in both dialects. Rows are written in the same transaction
as the chunk-row deletes:

- ``PurgeExecutionLogs`` queues the prefix ``logs/<exec>/``, due now, only when
  the execution had chunk rows with ``backend <> 'db'`` (counted before the rows
  are deleted). It counts as ``objects`` only the db objects deleted in the
  transaction. The response and the ``log.purge`` audit details also carry
  ``queued``: 1 when a prefix was queued, 0 otherwise (a purge of db-only chunks).
- Claiming an execution under a new attempt queues
  ``logs/<exec>/<previous-attempt>/``, due after a 2 minute grace, which
  outlives an in-flight ``Put`` of a stale writer, only when a previous attempt
  id exists and it had chunk rows with ``backend <> 'db'``. A first claim
  queues nothing.

A db-only installation therefore never queues anything.

**Sweeper.** ``internal/controller/logsweep`` runs on every controller (a
per-controller duty, not a leader duty) every 30 s with a batch of 50. For each
due entry it:

#. reads the clock and defers the entry with a compare-and-swap (the clock is
   read per entry; ``not_before`` = now + 60 s timeout + backoff ``min(30 s * 2^tries, 1 h)``, ``tries`` + 1), so one
   controller owns it;
#. refuses prefixes that do not match ``logs/<exec>/`` or
   ``logs/<exec>/<attempt>/``;
#. calls ``DeletePrefix`` on every external backend (60 s timeout);
#. deletes the row with a compare-and-swap on the deferred time.

On error the row stays and is retried after the backoff. ``s3blob.DeletePrefix``
drains the listing and returns an error if its context ended mid-listing, so a
cut-short delete is never reported as complete. A controller with no external
backend configured leaves the entries in the queue and logs a warning with the
entry count: the queue is global, and a controller without ``s3`` (rolling
restart, misconfiguration) must not drop entries for objects in ``s3``.

**Remaining orphans.** Objects stay under the execution prefix, and go with
that execution's purge, when a ``Put`` succeeded but ``InsertLogChunk`` failed,
or when a stale writer writes after the 2 minute grace (a refused insert after
a successful ``Put`` is cleaned up best-effort, see Flush). Until per-project log
retention exists, a bucket lifecycle rule is the recommended backstop
(:doc:`/reference/log-store`).

**Security.** No new secret bundle path: chunks are already masked by the agent
(:doc:`0047-agent-side-masking-only`). Secrets exist only in fields that
``Redacted()`` masks. Errors name the bucket and key, never the options; config validation messages do not echo ``logs.s3.endpoint``.

Alternatives Considered
-----------------------

- **Delete from S3 first, then run the transaction.** The audit event and the
  delete would not be atomic: a crash between them loses one or the other.
- **An ``fs`` backend.** It cannot hold chunks across controllers, which breaks
  the HA read path.
- **One delete row per chunk.** Many rows for what one prefix delete does.

Decision Criteria
-----------------

The database stays the source of truth; purge stays audited in one
transaction; no new infrastructure; HA-safe without a leader.

Rationale
---------

A prefix queue written in the purge transaction makes the intent durable and
atomic with the audit event, and an idempotent sweeper makes the external
delete eventually consistent. A compare-and-swap claim lets any controller
work without a leader.

Consequences
------------

Positive
~~~~~~~~

- Large log volumes can leave the database; switching is a config change.
- Purge stays atomic with its audit event.

Negative
~~~~~~~~

- External objects disappear a short time after a purge, not within it.
- Orphans (see above) can remain until purge or a lifecycle rule.
- In a versioned bucket a delete only adds a delete marker; purged data stays as
  noncurrent versions unless a lifecycle rule expires noncurrent versions.

Risks
~~~~~

- A bucket that is permanently unreachable keeps entries retrying hourly.
- Entries stay queued while no controller has the external backend configured.

Follow-up
~~~~~~~~~

- Per-project log retention (ARCHITECTURE section 10) should use the same
  queue.

Validation
----------

Store tests on SQLite and Postgres, the ``logblob`` and ``s3blob`` tests, and
the sweeper and flush tests under ``internal/controller/``.

References
----------

- :doc:`0053-audit-events`
- :doc:`0047-agent-side-masking-only`
- :doc:`/reference/log-store`
