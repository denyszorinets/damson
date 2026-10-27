ADR-0010: River for background work
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Run advancement and maintenance tasks must execute on any controller,
survive crashes, and avoid concurrent advancement of the same run. A broker is
ruled out (:doc:`0005-no-message-broker`).

Decision
--------

Use **River** (``riverqueue/river``) with its Postgres driver, and its SQLite
driver for single-controller installs, for:

- ``run.advance{run_id}``, enqueued in the same transaction as the triggering
  change, with River uniqueness (one available or running job per run);
- maintenance jobs.

Execution dispatch to agents is **not** a River job: each controller claims
executions for its own sockets with ``SKIP LOCKED``. Singleton duties use
leader leases.

Alternatives Considered
-----------------------

In-house job table
~~~~~~~~~~~~~~~~~~

Disadvantages:

- Re-implements retries, uniqueness, scheduling and cleanup.

Broker-based queues (Asynq/Redis, NATS)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Extra infrastructure; enqueue cannot share the database transaction.

Decision Criteria
-----------------

Transactional enqueue, both databases, maturity, license (MPL-2.0).

Rationale
---------

Transactional enqueue removes a whole class of "state changed but job lost"
bugs, and River runs on the database Damson already requires.

Consequences
------------

Positive
~~~~~~~~

- Enqueue is atomic with run state changes.

Negative
~~~~~~~~

- River's schema and migrations sit beside Damson's.

Risks
~~~~~

- SQLite driver maturity. Mitigation: SQLite is dev and single-controller only;
  covered by the dual-database test suite.

Follow-up
~~~~~~~~~

- M2: ``run.advance`` on River.

Validation
----------

Store and orchestration tests on both databases; chaos test from M3.

References
----------

- :doc:`/architecture/design` §2, §8.3
