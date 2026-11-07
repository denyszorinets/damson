ADR-0054: Audit retention
=========================

:Status: Accepted
:Date: 2026-11-07

Issue :issue:`174`. Follow-up to :doc:`0053-audit-events`. Implements the audit
part of the retention duty in ARCHITECTURE section 8.4.

Context
-------

``audit_events`` is insert-only and grows without bound. Some operators must
keep audit data for a fixed period and then delete it; others must never lose
it. Deleting audit rows is itself a security-relevant act.

Decision
--------

**One setting.** ``audit.retention_days`` (``DAMSON_AUDIT__RETENTION_DAYS``,
controller and dev roles). ``0`` is the default and keeps events forever; a
positive value lets the retention duty delete events older than that many days.
Negative values and values above 36500 fail validation at start. Nothing is
deleted unless the operator sets it.

**A leased singleton duty.** ``internal/controller/retention`` runs under the
``retention`` leader lease (ADR-0039, ARCHITECTURE section 8.4), started only
when a retention is configured. Every controller runs the loop (once at start,
then hourly) but only the lease holder prunes. The cutoff therefore comes from
one clock at a time, and each batch transaction is fenced with
``Tx.CheckLeader``.

**Atomic, recorded deletion.** ``Tx.PruneAuditEvents(before, limit, now)``
deletes up to ``limit`` events older than the cutoff (oldest first) and, when it
deleted any, appends one ``audit.prune`` event in the same transaction (target
type ``instance``, target ``audit``, actor ``system:retention``; details
``deleted`` and ``before``, never any content of the deleted events). It fails
with ``ErrInvalid`` before deleting if the context has no actor. Batches are 200
rows, at most 500 per pass, so a backlog never holds a long transaction; a pass
writes one event per batch.

**Deleting is the only change.** No API removes or edits an audit event; this
method is the only code path that deletes audit rows.

Alternatives Considered
-----------------------

- *Every controller prunes, no lease.* Simpler, but any host with a wrong clock
  (RTC reset, bad NTP) could wipe the trail, and N controllers write N
  partial-count events. Rejected.
- *Prune event written after the delete.* A crash between the two destroys rows
  with no record. Rejected; the event is in the delete's transaction.
- *A DB-side job (pg_cron, triggers).* Not available on SQLite and adds
  infrastructure. Rejected.
- *Per-project retention.* The trail is instance-wide today. Deferred.

Decision Criteria
-----------------

Safe by default; deletion is always recorded; works on SQLite and Postgres with
no new infrastructure; consistent with the existing singleton duties.

Rationale
---------

The leader lease already exists for this class of duty, and fencing the batch by
the lease term plus writing the event in the batch transaction gives a deletion
that is both single-writer and self-documenting.

Consequences
------------

Positive
~~~~~~~~

- Bounded audit table for operators who want it; unchanged behavior for others.
- Every deletion leaves an event.

Negative
~~~~~~~~

- Pruned events are gone. Export ``GET /api/v1/audit`` first, or keep ``0``.
- With retention on there is about one ``audit.prune`` event per non-empty pass
  per hour, and those expire too.

Risks
~~~~~

- A leader with a badly wrong clock can still prune too much; the lease limits
  this to one controller at a time, not to none.

Follow-up
~~~~~~~~~

- Run and log retention (the other parts of the duty) are not implemented.

Validation
----------

``storetest`` covers ``PruneAuditEvents`` (ordering, batching, atomic event,
refusal without an actor) on SQLite and Postgres. The duty tests cover pruning,
a backlog of several batches, fencing of a non-leader, ``0`` days and the loop.

References
----------

- :doc:`0053-audit-events`
- ARCHITECTURE sections 8.4 and 12.4
