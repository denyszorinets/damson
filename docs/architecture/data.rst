Data and flows
==============

The database is the only source of truth
(:doc:`decisions/0004-database-as-source-of-truth-without-consensus`). This page
summarizes the domain model and the main data flows; exact rules are in
:doc:`design` §4, §7, §8 and §10.

Domain model
------------

.. mermaid::

   erDiagram
     PROJECT ||--o{ PROJECT : "parent of"
     PROJECT ||--o{ JOB : contains
     PROJECT ||--o{ SECRET : scopes
     PROJECT ||--o{ ACCESS_RULE : carries
     JOB ||--|{ JOB_VERSION : "immutable versions"
     JOB ||--o{ SCHEDULE : has
     JOB ||--o{ WEBHOOK : has
     JOB ||--o{ RUN : invoked
     RUN }o--|| JOB_VERSION : pins
     RUN ||--|{ STEP_INSTANCE : contains
     STEP_INSTANCE ||--|{ EXECUTION : "one per attempt"
     EXECUTION ||--o{ LOG_CHUNK : "manifest rows"
     EXECUTION }o--o| AGENT : "runs on"

Global entities outside the project tree: **Node** (inventory, filtered per
project by its node scope), **Agent** (live connection), **ServiceAccount**,
**ApiToken** and **AuditEvent**.

Identity of work:

- A **StepInstance** has a stable ``path`` such as
  ``main/for_each[web-01]/install``. ``engine.BuildGraph`` uses the same paths as
  graph node ids.
- An **Execution** is one attempt with a unique ``attempt_id`` (UUIDv7), the
  fencing token (:doc:`decisions/0008-fencing-tokens-and-execution-leases`).

Run lifecycle
-------------

.. mermaid::

   sequenceDiagram
     autonumber
     participant T as Trigger (UI/API/schedule/webhook)
     participant C as Controller
     participant DB as PostgreSQL
     participant A as Agent
     T->>C: start run(options)
     C->>DB: tx: validate options, resolve targets, check concurrency,<br/>insert run, enqueue run.advance
     C->>DB: run.advance: load state, Decide(), apply actions (CAS on runs.version)
     C->>DB: dispatch: claim queued execution (SKIP LOCKED), set attempt_id, lease
     C->>A: step.execute(resolved input, secret bundle)
     A-->>C: log.append (batched)
     C-->>A: log.ack (after chunk commit)
     A->>C: agent.heartbeat (renews leases)
     A->>C: step.complete (retried until acked)
     C->>DB: CAS on attempt_id → terminal, enqueue run.advance
     C-->>A: ack

Execution states
~~~~~~~~~~~~~~~~

Transitions are compare-and-swap updates guarded by the expected state and
``attempt_id``. The diagram shows the transitions named in the specification;
the exact state column values are fixed by the M2 schema.

.. mermaid::

   stateDiagram-v2
     [*] --> queued: ScheduleExecution (scheduled_at = now + backoff)
     queued --> dispatched: claimed by a controller
     dispatched --> queued: agent rejects or send fails
     dispatched --> terminal: step.complete (matching attempt_id)
     dispatched --> lost: lease expired (reaper)
     dispatched --> cancelled: step.cancel acknowledged
     terminal --> [*]
     lost --> [*]: engine may schedule a new attempt
     cancelled --> [*]

The run view adds derived statuses (pending, running, skipped, retry-wait)
computed from step instance and execution state.

Log flow
--------

plugin → emitter → masker → agent batcher (200 ms / 64 KiB) → ``log.append`` →
owning controller → ring buffer (live tail) and chunk writer (5 s / 1 MiB) →
LogStore + ``log_chunks`` row → ``log.ack``.

- Unacked batches stay in agent memory (32 MiB), then a local spool (256 MiB).
  Overflow drops the oldest batches and logs a gap.
- Readers use ``GET /api/v1/executions/{id}/logs?after_seq=N`` (SSE): stored
  chunks first, then live tail, proxied to the owning controller when needed.

See :doc:`decisions/0016-pluggable-log-store-with-database-default`.

Persistence rules
-----------------

- Every store method runs on PostgreSQL and SQLite and is tested on both
  (:doc:`decisions/0003-postgresql-as-the-only-required-infrastructure`).
- Portable SQL in ``queries/shared``; dialect overrides only when unavoidable;
  JSON manipulation in Go (:doc:`decisions/0011-sqlc-and-goose-for-dual-dialect-sql`).
- Resolved step inputs are stored for audit and replay **without** secret
  values; only ``secret://`` refs are persisted.
- Outputs are JSON, schema-validated, at most 256 KiB per execution.
- Retention is per project (default 30 days), run by the retention duty.
