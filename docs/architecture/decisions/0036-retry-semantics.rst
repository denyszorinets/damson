ADR-0036: Retry semantics
================================================================================

:Status: Accepted
:Date: 2026-11-02

Context
-------

Steps can fail for reasons that go away: a dropped connection, a host that
restarts, an agent that dies mid-step (issue #85). The job spec reserves a
``retry`` block (ARCHITECTURE 5.2, 5.3 rule 7). The engine is pure and has no
randomness (:doc:`0009-pure-deterministic-engine`), attempts are fenced by
``attempt_id`` (:doc:`0008-fencing-tokens-and-execution-leases`), and until now
the store finished a step in the same transaction that recorded a failed
attempt, which leaves nothing for the engine to decide.

Decision
--------

Who finishes a step
~~~~~~~~~~~~~~~~~~~

- The store settles a step in the completion transaction only for a
  ``succeeded`` or ``canceled`` attempt. A ``failed``, ``timed_out`` or ``lost``
  attempt (completion, reaper) is recorded, ``runs.version`` is bumped, and the
  step stays pending.
- The engine then decides, for every step with or without ``retry``: schedule
  attempt n+1 (``ScheduleExecution``), or emit ``SettleStep`` so the store
  finishes the step from that attempt's outcome. The Decide that emits either
  waits; the advance enqueues itself again, and the next Decide reads the
  settled step.
- The store checks every attempt it is given: it must be number n+1 after a
  finished attempt n of an open step, and a settle must name the latest
  finished attempt; otherwise ``ErrConflict``. ``UNIQUE(step_instance_id,
  attempt_no)`` is the last guard. A duplicated or stale decision therefore
  cannot create an attempt twice.

When a failed attempt is retried
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

In this order, the first rule that says "no" settles the step with the
attempt's own error:

#. The run is being cancelled (a user cancel, or the job timeout), whether the
   attempt is a lost one or the step is an ``always()`` cleanup step. No step
   is retried during a cancel.
#. The step has no ``retry`` block: one attempt, as before, even for an
   idempotent plugin.
#. The attempt failed or was lost (``timed_out`` is a failure of kind
   ``timeout``).
#. ``latest.attempt < max_attempts`` (``max_attempts`` counts the first
   attempt).
#. A lost attempt is retried only if ``on_lost`` is true, or ``on_lost`` is
   unset and the plugin declares itself ``idempotent``. ``on_lost: false``
   forbids it even for an idempotent plugin. The idempotent flag is read from
   the stored manifests when the decision is made; a plugin without a manifest
   is not idempotent.
#. ``retry_on`` is true. It is a CEL condition over ``error`` (``step``,
   ``kind``, ``message`` and, when known, ``exit_code``); the default is
   ``error.kind in ['connection', 'timeout', 'lost']``. An expression that fails
   to evaluate means no retry, so ``catch`` and ``continue_on_error`` still see
   the real kind.
#. The retry would be due at or after the job-level timeout deadline: fail
   with the real error instead of a later "job timed out" cancel.

``continue_on_error`` and ``catch`` see only the settled, final error. The step
input is resolved once, at creation; secrets are resolved again at each
dispatch.

Backoff
~~~~~~~

- ``delay = min(max, initial * multiplier^(n-1)) * (1 - jitter * u)`` after
  failed attempt n, so the first retry waits ``initial`` and jitter only
  shortens. Defaults: ``initial`` 1s, ``multiplier`` 2, ``max`` 5m, ``jitter``
  0.2.
- The retry is due at the failed attempt's ``finished_at`` plus the delay (or
  at ``now`` when ``finished_at`` is missing), not at ``now``: the result is the
  same however late the advance runs.
- ``u`` is a hash of ``run_id``, step path and attempt number mapped to
  ``[0, 1)``. This honours "no randomness" (:doc:`0009-pure-deterministic-engine`):
  the same state gives the same schedule, controllers racing on one run emit
  identical actions, and different runs and steps still spread. The random
  source is injected at function level (``backoffDelay`` takes ``u``).
- No timer exists: the retry is a queued execution with a future
  ``scheduled_at``, which the claim loop skips until it is due, so it survives
  a controller crash.

Job spec
~~~~~~~~

``retry`` is allowed on plugin steps. ``max_attempts`` is required (1 to 100);
``backoff.initial`` and ``backoff.max`` are positive Go durations of at most
24h with ``max >= initial``; ``multiplier`` is 1 to 10; ``jitter`` is 0 to 1;
``retry_on`` is compile-checked; ``on_lost`` is an optional boolean.

Alternatives Considered
-----------------------

Skipping the settle in the store when the step has ``retry``
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

That puts job-spec semantics in the store, against the pure-engine rule.
Rejected.

Random jitter drawn at decision time
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

It would make two controllers racing on one run emit different schedules and
the goldens unstable. Rejected for the deterministic hash.

Consequences
------------

Positive
~~~~~~~~

- Retries survive controller crashes and failover with no in-memory state, and
  the fencing rules are unchanged: each claim rotates the ``attempt_id``, and a
  late report of an earlier attempt is stale.
- The engine owns all of the policy; the store only validates the sequence.

Negative
~~~~~~~~

- Every failed attempt costs one extra ``run.advance`` (the settle).
- ``error.exit_code`` is never set for an agent failure until the protocol
  carries it (issue #95); use ``has(error.exit_code)`` in ``retry_on``
  meanwhile.
- During a rolling upgrade a controller without this change treats ``retry`` as
  a reserved field: it cannot parse a job that uses it, and fails the run. Do
  not create or run jobs with ``retry`` until every controller runs this
  version. For jobs without ``retry`` an old controller waits on a pending step
  whose latest attempt failed (an empty advance), and a new controller picks it
  up through the sweep within one interval; completions an old controller
  handles still finish the step.
