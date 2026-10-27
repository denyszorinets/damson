ADR-0013: JSON job specs as versioned data
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Jobs must be editable in a UI, manageable as code from a folder (and later
Git), auditable and reproducible: a rerun must use exactly the definition the
original run used.

Decision
--------

- A job spec is a **JSON** document (``apiVersion: damson.dev/v1``,
  ``kind: Job``) stored in the database. JSON is canonical; the CLI may accept
  other input formats and convert them
  (:doc:`0024-yaml-as-an-input-format`).
- Each change creates an immutable **JobVersion** with a checksum; every run
  pins exactly one version.
- ``managed_by`` records ownership: ``ui``, ``files:<source>``, or (future)
  ``git:<source>``. Source-managed jobs are read-only in the UI.
- ``damson apply`` sends a folder tree to ``POST /api/v1/apply``, which validates
  all-or-nothing, diffs by checksum and supports ``--prune`` and ``--dry-run``.
  Future Git sync feeds the same apply engine.

Alternatives Considered
-----------------------

YAML as the canonical format
~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Ambiguous typing; harder to validate and diff byte-for-byte; JSON Schema
  tooling and the UI editor work natively with JSON.

Jobs stored only in Git
~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Makes Git a runtime dependency; no UI editing.

A DSL or code-based pipelines (Groovy, Starlark)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Specs become programs: no static graph, no schema-driven UI, harder to secure.

Decision Criteria
-----------------

Reproducibility, schema validation, UI support, path to Git sync.

Rationale
---------

Data, not code, lets the server validate specs, build graphs and generate forms;
immutable versions make runs reproducible.

Consequences
------------

Positive
~~~~~~~~

- Reruns are exact; history is complete.

Negative
~~~~~~~~

- JSON is verbose to hand-write.

Risks
~~~~~

- Spec format changes are expensive; ask before changing it (``CLAUDE.md``).

Follow-up
~~~~~~~~~

- M2: spec v1 (linear); M9: ``apply`` and ``export``.

Validation
----------

Spec fixtures in ``testdata/`` validated in CI; apply idempotency tests.

References
----------

- :doc:`/architecture/design` §5, §13
