ADR-0035: Step and job timeout semantics
================================================================================

:Status: Accepted
:Date: 2026-11-02

Context
-------

``timeout`` exists on a step (per attempt) and on the job (the whole run), and
M3 has to enforce both (issue #84). The engine is pure and has no timers
(:doc:`0009-pure-deterministic-engine`), the agent can be cut off from its
controller, and a controller can die at any time
(:doc:`0008-fencing-tokens-and-execution-leases`). The semantics are part of
the job spec, so they are recorded here.

Decision
--------

Step timeout (per attempt)
~~~~~~~~~~~~~~~~~~~~~~~~~~

- The engine puts the timeout on the execution as data
  (``executions.timeout_ms``, whole milliseconds, rounded up) and the
  controller sends it as ``timeout_ms`` in ``step.execute``. The **agent
  enforces it** with a context deadline that starts when it accepts the
  execute and survives reconnects, so it works while the agent is partitioned.
  No controller or engine timer exists.
- A timed-out attempt reports ``timed_out`` with error code ``timeout``. The
  engine sees a failed step whose ``error.kind`` is ``timeout``; retry, catch
  and ``continue_on_error`` react to that like to any failure.
- When a deadline and a ``step.cancel`` race, the first cause of the context
  cancellation decides ``timed_out`` or ``canceled``; the controller applies
  what the agent reports.
- Known gap: an agent that keeps heartbeating while its executor is wedged has
  no backstop yet. A controller-declared timeout (a compare-and-swap at
  start + timeout + grace, like the reaper) can be added without a protocol
  change.

Job timeout (the whole run)
~~~~~~~~~~~~~~~~~~~~~~~~~~~

- The deadline is ``runs.started_at + spec.timeout``; time spent pending does
  not count.
- The **engine decides**: when the deadline has passed and no cancel was
  requested, ``Decide`` emits ``TimeoutRun`` and treats the run as cancelling
  from that instant in the same call. The store records it as a sticky cancel
  (``runs.cancel_requested_at`` with ``runs.cancel_reason = 'timeout'``) inside
  the advance's own version compare-and-swap. Controllers with skewed clocks
  therefore cannot disagree afterwards, and a user cancel that commits first
  wins (the advance then loses the version CAS and sees the cancel).
- From then on the ordinary cancel path applies (:doc:`/development/dispatch`):
  running attempts are flagged and cancelled by their owning controller,
  queued ones are cancelled, pending steps are cancelled with the reason
  ``job timed out``.
- Only steps whose ``when`` calls ``always()`` or ``cancelled()`` still run
  after the deadline. The timeout itself does not trigger ``failure()``
  handlers, because a cancelled step is not a failed one; a step that really
  failed (before the deadline, or a cancelled attempt that reports ``failed``)
  still does. Those cleanup steps are bounded by their own step timeout, not by
  the job timeout.
- Final status: ``failed`` if any step failed or was cancelled; ``succeeded``
  only if every step still succeeded or was skipped by its ``when``. There is no
  ``timed_out`` run status. The API shows ``cancel_reason: timeout``.
- The wake-up is a River job scheduled for the deadline, queued in the
  transaction that starts the run (so it cannot be lost without the start).
  River's scheduler moves scheduled jobs to available every 5 seconds, so the
  job runs up to about 5 seconds after the deadline (a cost of using the queue
  rather than a timer). If the database's clock is ahead of the controller's, the job snoozes until
  the controller's clock reaches the deadline (a snooze does not count as an
  attempt). If the wake job is lost, or the run started before this change,
  the sweep (:doc:`0010-river-for-background-work`) times the run out within
  one sweep interval.

Alternatives Considered
-----------------------

Engine-side step timeouts
~~~~~~~~~~~~~~~~~~~~~~~~~

The engine would compare ``now`` with the attempt's start and ask for a
cancel. It needs a wake-up per attempt, a reason on ``step.cancel`` (or a
controller rewriting ``canceled`` as ``timed_out``), and cannot stop anything
while the agent is unreachable. Rejected.

A ``runs.deadline_at`` column with a poll loop
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

One more column, an index, a query on both dialects and another loop to own.
The scheduled River job covers it with the existing queue. Rejected.

A ``timed_out`` run status
~~~~~~~~~~~~~~~~~~~~~~~~~~

Most explicit, but changes the API enum, the generated client and the UI for
what ``failed`` plus ``cancel_reason`` already says. Rejected for now.

Consequences
------------

Positive
~~~~~~~~

- Step timeouts hold under partitions and controller failover; the engine stays
  pure and timer-free.
- Job timeouts reuse the cancel path of :doc:`0008-fencing-tokens-and-execution-leases`
  and survive a controller crash, because the deadline is in the database.

Negative
~~~~~~~~

- Cleanup after a job timeout is unbounded except by its own step timeouts.
- Steps created after the cancel are told apart from in-flight ones by
  comparing creation time with ``cancel_requested_at`` (a step created at the
  same instant counts as created after it, which is how the cleanup step of the
  very advance that records the timeout survives). The two times can come from
  different controllers' clocks: with a large skew a cleanup step created by a
  controller that runs behind could be cancelled, as it could after a user
  cancel. Keep controller clocks in sync; a per-step marker is a possible
  follow-up.
- A queued execution cancelled by the timeout keeps the reason ``run
  cancelled``, while steps that never started say ``job timed out``.
- During a rolling upgrade a controller without this change ignores the wake
  job's ``wake`` argument and runs it at once, which spends it; the sweep then
  times the run out within one interval. Runs already past their deadline when
  the first upgraded controller sweeps are timed out then.
- A wedged executor on a live agent is not stopped until the lease logic or a
  future controller-side backstop handles it.
