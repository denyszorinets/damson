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

The agent is acknowledged after the commit.

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
``startOrchestration`` in ``cmd/damson`` wires advance, log ingest, the
dispatcher and the gateway, and starts the gateway last.

Not yet implemented
-------------------

* Step timeout (``timeout_ms``).
* Real inventory and secret store behind the nodes and secrets bundle of
  ``step.execute`` (M4). Until then only the dev-only ``--dev-nodes`` file
  serves them (ADR-0034, see :doc:`/reference/cli`).
* A notify hint for dispatch (the 1 s poll is the only trigger).
* ``step.cancel`` dispatch for running executions.
* Adoption on reconnect (issue #60, M3): ``agent.hello`` answers ``continue``
  only for a ``finished`` attempt that is still current and owned by the
  agent (it redelivers ``step.complete``); running attempts are answered
  ``cancel``, so a reconnect cancels running steps.
* Retries (M3).
