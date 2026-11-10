High availability
=================

Damson has no consensus protocol of its own. Controllers are interchangeable
and keep no durable state; PostgreSQL is the single source of truth, and every
state change is a compare-and-swap on a database row
(:doc:`/architecture/decisions/0004-database-as-source-of-truth-without-consensus`).
High availability therefore means: run two or more controllers on one
PostgreSQL that is itself highly available.

How it works
------------

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - Duty
     - Behaviour
   * - API and UI
     - Any controller serves any request. Put them behind one load balancer.
   * - Run advancement (``run.advance``)
     - A River job in the database that any controller can work. A periodic
       sweep (1 minute) re-enqueues running runs, so a run whose controller
       died moves again within one sweep interval.
   * - Dispatch, lease renewal, cancels
     - Every controller does this for the agents connected to it. Agents are
       claimed with row locks (``FOR UPDATE SKIP LOCKED``), so two controllers
       never dispatch the same execution.
   * - Singleton duties
     - One controller at a time holds a **leader lease** per duty, stored in
       the ``leader_leases`` table, renewed every 5 seconds with a 15 second
       TTL. The duties are ``reaper`` (marks executions with expired leases
       ``lost`` and removes dead ``controller_instances`` rows), ``scheduler``
       (fires schedules) and ``retention`` (prunes audit events, finished runs
       and expired sessions). A controller that cannot reach the database stops acting as
       leader before another can take over. Failover takes at most the TTL plus
       one renewal; a graceful stop hands the lease over at once.
   * - Log sweeper
     - Runs on every controller, not under a lease. It deletes S3 log objects
       that were queued for deletion.
   * - Agents
     - Connect out to any controller (through the load balancer) and reconnect
       with jittered exponential backoff of up to 30 seconds. A new controller
       adopts running attempts of a returning agent.
   * - Live log tail
     - A request to controller X for an execution that runs on controller Y is
       proxied to Y over the internal listener (``controller.internal_listen``,
       default ``:8081``), signed with the cluster secret. If that fails, the
       stream falls back to stored chunks and retries every 5 seconds.

Each duty is idempotent, so a short overlap of two leaders during a failover is
harmless. Details: :doc:`/development/dispatch`, ARCHITECTURE sections 8.3 to
8.5.

Requirements for a cluster
--------------------------

- PostgreSQL. A second controller on SQLite refuses to start with ``another
  controller is using this SQLite database; SQLite supports one controller, use
  PostgreSQL for several``.
- The same ``controller.cluster_secret`` on all controllers, at least 32 bytes.
  Without it the internal listener does not start and controllers cannot tail
  each other's logs. With PostgreSQL the configuration is rejected without it.
- A ``controller.internal_url`` per controller that the others can reach
  (on Kubernetes the chart uses the pod IP).
- The same KEK on all controllers, and the same ``engine.finally_grace``.
- Clocks that agree within a few seconds. Lease times use each controller's own
  clock; skew shifts failover time but does not cause flapping.
- A load balancer that supports WebSocket upgrades for ``/agent/v1/connect``.

Sizing
------

- **Controllers.** Two is the minimum for availability, three lets you lose
  one during an upgrade. The design budget is about 1000 agents on three
  controllers (ARCHITECTURE section 8.6): roughly 100 heartbeats per second in
  total, handled in memory, with database writes batched. Log ingestion is the
  real load, not sockets. This figure is a design target; it has not been
  measured in this repository.
- **Agent slots.** ``agent.slots`` is the number of concurrent executions per
  agent; ``0`` means the CPU count times four. Lower it for heavy plugins
  (``buildah``, ``ansible``) and use a separate pool for them. Capacity of a
  pool is the sum of its agents' slots.
- **Database connections.** Each controller opens its own pool (see
  :doc:`install`). Multiply by the controller count.
- **Log volume.** With the ``db`` log store all log chunks go through the
  database. For many concurrent runs use the S3 store
  (:doc:`/reference/log-store`).

Failure scenarios
-----------------

The HA chaos tests (``make chaos``, :repo:`internal/chaos`) run several
controllers on one PostgreSQL with agents that are moved or cut off. They pin
these behaviours.

.. list-table::
   :header-rows: 1
   :widths: 22 40 38

   * - Failure
     - What happens
     - What you see
   * - A controller crashes
     - Its agents reconnect to another controller, which adopts the running
       attempts before their leases (60 seconds) expire, so the steps are not
       lost. A run being advanced by the dead controller moves again within one
       sweep interval. If it held a leader lease, another controller takes it
       within the TTL plus one renewal. Its ``controller_instances`` row is
       removed by the reaper after a minute.
     - Brief 5xx from the load balancer until its health check removes the
       pod; the live tail of its executions is interrupted and continues from
       stored chunks.
   * - A controller stops gracefully
     - It deregisters, tells agents to reconnect elsewhere and releases its
       leader leases. The step finishes exactly once on the original attempt.
     - Agents reconnect within the jitter delay; no lost executions.
   * - Agent connection cut
     - The step keeps running on the agent. If it finishes while cut off, the
       result is held and delivered once on reconnect. A cancel requested
       meanwhile waits in the database and is sent by the controller that
       adopts the agent. If the agent does not return within the lease, the
       reaper marks the execution ``lost`` and the job's retry policy applies.
     - Agent ``disconnected`` in the Agents page; a WARN ``execution lost:
       lease expired`` if it did not come back.
   * - Agent host lost
     - As above, without a return: the lease expires and the execution is
       ``lost``.
     - WARN ``execution lost: lease expired`` with ``agent_id``.
   * - Database outage
     - Controllers cannot make progress: ``/readyz`` returns 503, API calls
       fail, leaders step down. Nothing is lost that was committed. When the
       database is back, controllers resume on their own.
     - ``/readyz`` 503, ERROR lines such as ``renewing leader lease``. What a
       long outage does to the 60 second execution leases is not covered by the
       chaos tests; check for ``lost`` executions afterwards.
   * - Controller cut off from the database only
     - It stops acting as leader before its lease can be taken over, so
       duties are never done by two controllers at once.
     - WARN ``leader lease lost``.

Cancel requests also work through a controller that does not own the step: the
flag is stored in the database and the owner finds it.

Run the chaos test against your own PostgreSQL version with
``DAMSON_TEST_POSTGRES_DSN`` (see ``make chaos``).
