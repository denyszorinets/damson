ADR-0004: Database as the source of truth, coordination without consensus
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Several controllers serve the same installation behind a load balancer with no
stickiness. They must agree on who dispatches which execution, who runs
singleton duties (scheduler, reaper, retention), and which run state is
current, while any controller may die at any time.

Decision
--------

- **The database is the only source of truth.** Controllers hold only caches and
  live sockets; no durable state lives in memory.
- **There is no consensus protocol inside Damson.** Coordination uses:

  - row-level compare-and-swap: ``UPDATE … WHERE state = $expected`` with a
    rows-affected check for every state transition;
  - ``SELECT … FOR UPDATE SKIP LOCKED`` to claim queued executions (Postgres);
  - one **leader lease** row per singleton duty
    (``leader_leases(name, holder, expires_at)``, renewed every 5 s, TTL 15 s).

- Singleton duties are idempotent, so a brief double-leader overlap is harmless.
- Run advancement uses optimistic concurrency on ``runs.version``.
- Controllers register in ``controller_instances`` for cross-controller routing
  (log tail proxy, cancel hints).

Alternatives Considered
-----------------------

Raft-based storage or embedded Raft (rqlite, dqlite, hashicorp/raft)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- No external database.

Disadvantages:

- Controllers become stateful and can no longer be bounced freely.
- Losing quorum stops all writes.

Sticky ownership in memory with gossip
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Split-brain risks; state lost on crash; hard to test.

Decision Criteria
-----------------

Correctness under failure, operational simplicity, testability.

Rationale
---------

Postgres already provides linearizable row updates. Building on CAS and leases
gives correct failover without a second consensus system, and leaves controllers
disposable.

Consequences
------------

Positive
~~~~~~~~

- Any controller can be killed at any time; another picks up on the next tick.
- Coordination is visible and debuggable in SQL.

Negative
~~~~~~~~

- Every transition costs a database round trip; batching (lease renewals once
  per 10 s per controller) is required at scale.
- Failover latency is bounded by lease TTLs (15 s duties, 60 s executions).

Risks
~~~~~

- Forgetting a rows-affected check silently breaks safety. Mitigation: hard
  rule 2 and review.

Follow-up
~~~~~~~~~

- M3: leader leases, reaper, multi-controller on Postgres.

Validation
----------

HA chaos test (from M3): 3 controllers, 5 agents, 200 jobs with random kills;
every run reaches a terminal state and no execution completes twice.

References
----------

- :doc:`/architecture/design` §8, §23
- :doc:`0008-fencing-tokens-and-execution-leases`
