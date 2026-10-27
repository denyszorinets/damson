ADR-0003: PostgreSQL as the only required infrastructure, SQLite for dev
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Damson must be highly available from day one (any controller or agent can be
bounced without losing runs), yet simple to deploy, including air-gapped.
It needs durable storage for jobs, runs, executions, logs (by default), secrets
(by default), sessions and audit events, plus queueing and coordination between
controllers. Developers and small installs want a zero-dependency mode.

Decision
--------

- **PostgreSQL** is the only required infrastructure in production and the
  only supported backend for multi-controller (HA) installs. Driver: ``pgx``.
- **SQLite** (``modernc.org/sqlite``, pure Go) is supported for development and
  single-controller installs. Exactly one controller may run against SQLite,
  enforced with a lock row at startup.
- Every store method is tested on both databases (hard rule 4 in ``CLAUDE.md``).
- Object storage (logs) and OpenBao (secrets) are optional add-ons, never
  required.

Alternatives Considered
-----------------------

PostgreSQL only
~~~~~~~~~~~~~~~

Advantages:

- One dialect; no portability tax.

Disadvantages:

- ``damson dev`` and small installs would need a Postgres server.

MySQL/MariaDB support
~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- No ``LISTEN/NOTIFY``; weaker fit for River; a third dialect to test.

Embedded Raft store (rqlite, dqlite)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Rejected in :doc:`0004-database-as-source-of-truth-without-consensus`.

Decision Criteria
-----------------

Operational complexity, HA behaviour, developer experience, testing cost.

Rationale
---------

Postgres offers the primitives the design needs (row locks,
``FOR UPDATE SKIP LOCKED``, ``LISTEN/NOTIFY``, advisory locks) and is already
operated by most target organisations. SQLite gives a one-command dev setup at
the cost of a second dialect, which is bounded by keeping SQL portable.

Consequences
------------

Positive
~~~~~~~~

- One external dependency to operate, back up and secure.
- ``damson dev`` needs nothing installed.

Negative
~~~~~~~~

- Two dialects: dual migrations, per-dialect query overrides, double test runs.
- SQLite installs cannot scale out or be HA.

Risks
~~~~~

- Dialect differences slipping through. Mitigation: shared test suite on both,
  JSON manipulation in Go rather than SQL.
- Database load from logs at scale; mitigated by the optional S3 log store
  (:doc:`0016-pluggable-log-store-with-database-default`).

Follow-up
~~~~~~~~~

- M0: both store backends with migrations; CI runs tests on both.

Validation
----------

M0: store test suite green on SQLite and Postgres (testcontainers). M3: HA
chaos test on Postgres.

References
----------

- :doc:`/architecture/design` §2, §3, §8.1
- :doc:`0011-sqlc-and-goose-for-dual-dialect-sql`
