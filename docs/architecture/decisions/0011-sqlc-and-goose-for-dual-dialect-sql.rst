ADR-0011: sqlc and goose for dual-dialect SQL
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

The store supports PostgreSQL and SQLite
(:doc:`0003-postgresql-as-the-only-required-infrastructure`). Every state
transition is a hand-written CAS ``UPDATE`` whose exact SQL matters. Damson
prefers generated code over hand-written boilerplate.

Decision
--------

- Queries are written in SQL and compiled to Go with **sqlc**. Portable SQL
  goes in ``internal/store/queries/shared``; dialect-specific SQL goes in
  ``queries/postgres`` or ``queries/sqlite`` only when unavoidable.
- JSON manipulation happens in Go, not SQL.
- Migrations use **goose**, with separate directories per dialect
  (``migrations/{postgres,sqlite}``). ``damson migrate up`` runs them
  explicitly, or controllers run them at start behind an advisory lock with
  ``--auto-migrate``.
- A ``Store`` interface is implemented by ``store/postgres`` and
  ``store/sqlite``, with sentinel errors ``ErrNotFound``, ``ErrConflict`` and
  ``ErrStale``.
- Generated code is never hand-edited (hard rule 6).

Alternatives Considered
-----------------------

An ORM (GORM, ent)
~~~~~~~~~~~~~~~~~~

Disadvantages:

- Hides the exact SQL of CAS transitions; runtime reflection; dialect quirks
  surface late.

Query builder (squirrel, goqu)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Hand-written scanning code; no compile-time checking against the schema.

Decision Criteria
-----------------

Control over SQL, compile-time checking, both dialects, code volume.

Rationale
---------

sqlc keeps SQL explicit and reviewable while generating the boring parts.

Consequences
------------

Positive
~~~~~~~~

- Type-safe queries checked against the schema at generate time.

Negative
~~~~~~~~

- Two migration trees to keep in step.

Risks
~~~~~

- Shared queries that behave differently per dialect. Mitigation: one test
  suite run on both.

Follow-up
~~~~~~~~~

- M0: ``make gen`` and both migration trees.

Validation
----------

M0: ``make test-pg`` and SQLite store tests green.

References
----------

- :doc:`/architecture/design` §2, §16, §19
