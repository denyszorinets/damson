Store and migrations
====================

The store is the persistence boundary (``internal/store``). The database is the
only source of truth: controllers keep no durable state, and every state
transition is a compare-and-swap (CAS) with a rows-affected check.

Layout
------

.. code-block:: text

   internal/store/
     store.go             Store interface and sentinel errors
     migrations/          embedded goose migrations: postgres/*.sql, sqlite/*.sql
     queries/shared/      sqlc queries that are portable to both dialects
     queries/postgres/    Postgres-only queries (only when unavoidable)
     queries/sqlite/      SQLite-only queries (only when unavoidable)
     postgres/            pgx v5 backend, generated code in postgres/gen
     sqlite/              modernc.org/sqlite backend (pure Go), generated code in sqlite/gen
     storefactory/        Open(config.Database) picks the backend
     storetest/           the contract suite both backends run

``storefactory`` is a separate package so the backends can import ``store``
for its sentinel errors without an import cycle.

Sentinel errors
---------------

``store.ErrNotFound``, ``store.ErrConflict`` and ``store.ErrStale``. Backends
wrap them with ``%w``; callers use ``errors.Is``. A failed CAS returns
``ErrStale`` when the row exists and ``ErrNotFound`` when it does not.

Migrations
----------

Migrations are plain SQL files run by goose, tracked in the
``schema_migrations`` table. Postgres and SQLite have separate directories
because DDL differs; keep the two in step. ``Store.Migrate`` applies pending
migrations. On Postgres it holds a session advisory lock, so several
controllers can call it at start without racing. ``Store.MigrationVersion``
reports the applied version, for the readiness check.

Add a migration by creating the next ``NNNNN_name.sql`` in **both** directories
with ``-- +goose Up`` and ``-- +goose Down`` sections, then run ``make gen``.

sqlc
----

``sqlc.yaml`` generates one package per dialect from the migrations (as the
schema) and ``queries/shared`` plus the dialect directory. Write shared queries
with ``sqlc.arg(name)`` so they parse on both engines. Keep JSON handling in Go.
``make gen`` regenerates the code; never edit files under ``*/gen`` by hand.
sqlc is pinned by the ``tool`` directive in ``go.mod`` and runs as
``go tool sqlc``, so no separate install is needed.

Configuration
-------------

``database.driver`` is ``sqlite`` or ``postgres``; ``database.dsn`` is a file
path (SQLite, opened in WAL mode with foreign keys on) or a ``postgres://`` URL.
Open errors never include the DSN.

Testing
-------

``storetest.Run`` is one suite invoked per backend, so every store method is
tested on both. SQLite runs in ``make test``. Postgres tests carry the
``integration`` build tag and run with ``make test-pg``, which starts
``postgres:17-alpine`` with testcontainers. To reuse a server instead, set
``DAMSON_TEST_POSTGRES_DSN``; each test gets its own database. When neither a
container runtime nor the variable is available the Postgres tests print a
``SKIP`` line and pass, so check the output before trusting a green result.
