ADR-0058: Schedules and the scheduler duty
==========================================

:Status: Accepted
:Date: 2026-11-07

Issue :issue:`194`. Implements the schedule trigger of ARCHITECTURE section 12.3
and the scheduler duty of section 8.4 (:doc:`0039-leader-lease`).

Context
-------

Jobs must start on a timetable, exactly once per fire time, with several
controllers running. ARCHITECTURE section 12.3 first proposed a
``schedule_fires`` table with a unique ``(schedule_id, fire_at)`` to get
exactly-once.

Decision
--------

**Table.** Migration 00014 adds ``schedules(id, job_id, name, cron, timezone,
options, enabled, misfire, next_fire_at, last_fire_at, last_run_id, last_error,
version, created_at, updated_at)``. ``job_id`` is a foreign key with cascade
delete; ``name`` is unique per job; ``options`` is JSON; ``misfire`` is
``run_once`` or ``skip``.

**Duty.** The scheduler is a leader-lease duty named ``scheduler``. It ticks
every 10 seconds (node ``Timing.Schedule``) and selects enabled schedules with
``next_fire_at <= now``, at most 100 per tick.

**One transaction per fire.** Each fire:

1. ``CheckLeader`` with the lease term.
2. Creates the run through the same path as the API trigger (``api.Starter``):
   options are validated against ``spec.options`` with defaults applied, the
   targets are frozen, and ``run.advance`` is enqueued.
3. ``FireSchedule``: ``UPDATE ... WHERE id AND enabled AND version = <version read> AND next_fire_at =
   <value read>``, moving ``next_fire_at`` to the next time after ``now`` and
   setting ``last_fire_at``, ``last_run_id`` and ``last_error``.

Exactly-once comes from that compare-and-swap. If it matches no row the
transaction rolls back, run included. The run's trigger JSON is
``{"type":"schedule","schedule_id":..,"fire_at":<RFC 3339>}``.

**Misfire.** If a fire time is more than 1 minute past due, ``run_once`` starts
one run (it does not catch up each missed time); ``skip`` starts none and sets
``last_error`` to ``skipped a missed fire time (misfire: skip)``. Either way
``next_fire_at`` becomes the next time after ``now``.

**Failures at fire time.**

- Options no longer valid (the job spec changed): no run; ``last_error`` lists
  field locations only, never option values; the schedule still advances.
- Transient errors (database): nothing is written, the schedule stays due and
  is retried on the next tick.
- The expression can no longer be evaluated: it fires this time, ``last_error``
  is set, and the scheduler looks again in 1 hour.

**Cron.** Five fields, or one of ``@hourly``, ``@daily``, ``@weekly``,
``@monthly``, ``@yearly``, ``@midnight``. No seconds field, no ``@every``, no
embedded ``TZ=`` or ``CRON_TZ=``. The zone is a separate IANA ``timezone``
field (default ``UTC``). The parser is ``robfig/cron/v3`` (MIT; a new direct
dependency). The zone database is embedded with ``time/tzdata``, so it does not
depend on the host.

**Daylight saving time.** An expression that names the hour fires once when the
clocks go back (the first occurrence) and is skipped for the day when its time
does not exist (spring forward). An expression with a wildcard hour (``*``,
``*/n``, ``@hourly``) runs through the change normally.

**API.** Admin token until M8 (amended by ADR-0076: project roles). ``GET`` and ``POST /schedules``; ``GET``,
``PATCH`` and ``DELETE /schedules/{id}``; list filter ``job_id``; cursor
paging. ``PATCH`` takes an optional ``if_version`` (409 on mismatch). Options
are validated against the job's current ``spec.options`` on create and patch.
``next_fire_at`` is recomputed when ``cron`` or ``timezone`` change or a
schedule is re-enabled; a disabled schedule has none.

**Audit.** ``schedule.create``, ``schedule.update`` and ``schedule.delete``,
target type ``schedule``, actor ``admin-token:<12 hex>``. Details are
``job_id``, ``name``, ``cron``, ``timezone``, ``enabled``, ``misfire`` and
``version``; options are left out because they may name secret refs. The
scheduler's own actor is ``system:scheduler``; fires are not audited, because
the run records its trigger.

**Scope.** Schedules are controller-wide, not per project, until M8.

Alternatives Considered
-----------------------

- *A ``schedule_fires`` table, unique on ``(schedule_id, fire_at)``* (the
  original section 12.3 text). Works, but adds a table that grows without bound
  and needs its own pruning. Rejected; the CAS on ``next_fire_at`` gives the
  same guarantee.
- *Catch up every missed fire time.* Can start a burst of runs after downtime.
  Rejected; ``run_once`` and ``skip`` cover the real needs.
- *Seconds, ``@every`` and embedded time zones in the expression.* More ways to
  write the same thing and to start jobs too often. Rejected.

Decision Criteria
-----------------

Exactly-once per fire time; no new infrastructure; works on SQLite and
Postgres; the same run creation path as the API trigger.

Consequences
------------

Positive
~~~~~~~~

- No extra table to grow or prune; a fire is one transaction.
- Runs from a schedule behave exactly like API-triggered runs.

Negative
~~~~~~~~

- Fire time accuracy is the tick, about 10 seconds.
- The fire history is only ``last_*`` on the schedule and the runs themselves.

Risks
~~~~~

- A job spec change can make a schedule's stored options invalid. The schedule
  then makes no runs and shows the problem in ``last_error`` until fixed.

References
----------

- :doc:`0039-leader-lease`
- :doc:`0053-audit-events`
- ARCHITECTURE sections 8.4, 12.3, 12.4 and 14

The version check also cancels a fire when the schedule was edited after the
scheduler read it, and a fire bumps the version so an edit based on the
pre-fire row cannot write the old ``next_fire_at`` back. An hour field written
as a list or range (``0-23``) counts as naming the hour, like Vixie cron.
