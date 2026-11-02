Dispatch, completion and the reaper
===================================

``internal/controller/dispatch`` implements steps 3 and 4 of the run lifecycle
(ARCHITECTURE section 8.3) and the lease rules of section 7.3. Dispatch is not
a River job: it is a loop owned by the ``Dispatcher`` component (``Start`` and
``Stop``, goleak clean). Applying the result goes back through ``run.advance``
(see :doc:`orchestration`).

Dispatch loop
-------------

Every second, for each locally connected agent with free slots:

#. ``ClaimExecutions`` by the agent's pool (see :doc:`store`). The claim gives
   each execution a new ``attempt_id`` and a lease (TTL 60 s).
#. Resolve the plugin through ``pluginreg`` and check that the agent is
   capable (name, version and ``Requires``).
#. Send ``step.execute`` with the plugin's resolved name and version and the
   step input.
#. On accept, ``MarkExecutionRunning``.

The claim filters by pool only; plugin and capability matching happens after
it. An execution claimed by an agent that cannot run it is requeued with a 5 s
delay (known limitation: incapable agents in the pool churn it).

Definite failures (agent rejected, plugin unresolved) requeue at once with a
CAS under the ``attempt_id``. Delivery is at-most-once on an ambiguous send: if
``step.execute`` times out or the socket closes after the write, the controller
sends ``step.cancel`` for the attempt and requeues only if the cancel is
answered. Otherwise the execution stays ``dispatched`` and the lease reaper
marks it ``lost``; the agent also tombstones a cancelled attempt, so a late
execute is rejected.

Dispatch rounds run per agent asynchronously, one in flight per agent. After a
transport failure the rest of that agent's claimed batch is requeued
immediately.

Slots are a soft in-memory count: seeded from ``max_concurrent`` in
``agent.hello`` and refreshed by ``free_slots`` in each heartbeat. The agent's
"no free slot" rejection is the authority; the count only avoids pointless
claims, so "never dispatches beyond free slots" is best effort: the count can
briefly exceed reality.

``step.complete``
-----------------

The handler is fenced by ``attempt_id`` and by the owning ``agent_id``. An
unknown, foreign or already finished attempt is acknowledged as ``stale`` and
changes nothing. Otherwise, in one transaction:

* ``Tx.CompleteExecution`` (CAS on the attempt);
* the step instance moves to ``succeeded``, ``failed`` or ``canceled`` with its
  outputs or error (this fulfils the contract that the store flips the step
  status, on which the engine's ``waitOn`` relies);
* ``runs.version`` is bumped and a River ``run.advance`` job is enqueued.

The agent is acknowledged after the commit. After the commit the dispatcher also
asks the log ingest to ``Finish`` the execution (``Config.Finish``), so its
buffered lines are flushed and readers of the finished execution see them at
once instead of after the periodic flush.

Lease renewal
-------------

Every 10 s each controller makes one ``RenewLeases`` call for the union of the
``running_attempts`` of its local agents' heartbeats. The lease TTL is 60 s.

Reaper
------

Every 10 s ``Tx.ExpireLeases`` marks expired leases ``lost``, fails their
steps and enqueues ``run.advance``. For now the reaper runs on every
controller; the CAS per execution on ``attempt_id`` and lease makes overlap
harmless. Lease expiry is judged with each controller's own clock, so the
fastest clock decides: keep controller clocks in sync. The leader lease of
ARCHITECTURE section 8.4 arrives in M3.

Lock order
----------

The reaper and complete transactions touch rows in a fixed order: step rows
first, then each distinct run once, sorted by run id.

Gateway hooks
-------------

The gateway exposes ``SessionHook`` / ``AddHook``: log ingest and the
dispatcher install their handlers per socket. ``Agents()``, ``Reserve()`` and
``Release()`` let the dispatcher list connected agents and hold their slots.
``node.Start`` in ``internal/controller/node`` wires the gateway, advance, log
ingest, the dispatcher and the HTTP API in that order, with the production hook
and stop order. ``cmd/damson`` and the HA chaos test both run it; ``node.Timing``
shortens the background intervals for tests, and ``Timing.LeaseTTL`` is the one
lease length used for dispatch and for adoption on reconnect.

Adoption on reconnect
---------------------

``agent.hello`` reconciliation answers ``continue`` for a ``finished`` attempt
that is still current and owned by the agent (it redelivers ``step.complete``)
and for a ``dispatched`` or ``running`` attempt of the agent. All of them are
adopted by the controller in one call to ``ExecutionStore.AdoptExecutions``. The adoption moves
``controller_id`` and extends the lease by ``Config.LeaseTTL``, in one
statement that also checks the agent row still names this controller at this
``session_seq``; a newer session makes it a no-op and the attempt is answered
``cancel``. Other controllers' ``RenewLeases`` no longer match, so the old
controller cannot keep the lease alive. Any store error fails the hello.

Cancel
------

A cancel request reaches running steps in two parts (ARCHITECTURE 8.3 step 7).
``run.advance`` decides which executions to cancel: a queued one is cancelled
with its step, a ``dispatched`` or ``running`` one gets
``executions.cancel_requested_at``. The flag is set whatever the attempt id,
because a claim between the advance's read and its apply rotates the attempt,
and the engine stops emitting the cancel once the flag is in the tree. A queued
row never carries the flag.

Every ``CancelInterval`` (2 seconds) each controller lists the flagged
attempts it owns (``controller_id``) and sends ``step.cancel`` with
``CancelGrace`` (5 seconds) to the agents it has a socket to, one goroutine per
agent. An answered attempt is not asked again (an in-memory cache; losing it
causes a harmless resend). ``known=false`` changes nothing in the database: the
agent's report or the lease reaper settles it. The agent stops the plugin and
reports ``canceled``; a plugin that finishes anyway reports ``succeeded``, which
is applied as reported.

If the agent is away, no controller has a socket to it and the flag waits. The
controller that adopts the agent on reconnect (see above) owns the attempt and
sends the cancel on its next poll; reconciliation itself still answers
``continue``, so a result the agent finished is not thrown away.

A flagged attempt that never reached the agent (the agent rejected it, the
send failed, or its cancel was answered first) is not requeued:
``RequeueExecution`` refuses a flagged row and the dispatcher ends it with
``Tx.CancelUndelivered``, which makes it and its step ``canceled`` and wakes the
run. A running attempt is never ended this way.

There is no notify hint yet; the poll is the only trigger.

Not yet implemented
-------------------

* Step timeout (``timeout_ms``).
* Real inventory and secret store behind the nodes and secrets bundle of
  ``step.execute`` (M4). Until then only the dev-only ``--dev-nodes`` file
  serves them (ADR-0034, see :doc:`/reference/cli`).
* A notify hint for dispatch and cancel (the 1 s and 2 s polls are the only triggers).
* Retries (M3).
