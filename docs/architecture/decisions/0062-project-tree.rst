ADR-0062: Project tree
======================

:Status: Accepted
:Date: 2026-11-08

Issue :issue:`203`. Implements ARCHITECTURE section 12.1.

Context
-------

Projects were flat names. M8 needs a hierarchy for access rules, node scope
and secret scopes. Secret scopes already use paths (ADR-0042), and several
records are keyed by project path.

Decision
--------

**Table.** Migration 00017 (both dialects) renames ``projects.name`` to
``path`` and adds ``parent_id`` (NULL for roots, foreign key), ``description``,
``version`` (compare-and-swap) and ``updated_at``, plus the index
``projects_parent(parent_id, path)``. Existing flat names become roots;
``default`` is a root.

**Path grammar.** The secret scope grammar of ADR-0042: 1 to 8 segments, each
matching ``^[a-z0-9][a-z0-9-]{0,62}$``. ``_inventory`` cannot be a project.

**Immutable paths.** There is no move or rename in M8. Literal ``secret://``
refs in specs, ``secrets.scope_path``, the service token project scope and
audit ``project_path`` are all keyed by path. Moving is deferred to a future
ADR.

**Subtree.** ``path = p OR (path >= p||'/' AND path < p||'0')``. It is portable
across SQLite and PostgreSQL and uses the path index. ``infra`` does not
contain ``infrastructure``.

**Store.** ``ProjectStore``: ``GetProjectByPath``, ``GetProject``,
``ListProjects``, ``ListProjectSubtree``, ``CreateProject`` (the parent must
exist; there is no ``mkdir -p``), ``UpdateProject`` (compare-and-swap on
``version``; description only) and ``DeleteProject``. Delete gives a conflict
that names the reason: child projects, jobs, secrets in its scope path, or
non-revoked service tokens scoped exactly to it. The last stops a recreated
path from inheriting old tokens.

**Audit.** Writes are audited in the same transaction: ``project.create``,
``project.update`` and ``project.delete``, target type ``project``,
``project_path`` the path, details ``{path, description}``.

**API (admin token until the authorization slice, #205).** ``GET`` and ``POST
/api/v1/projects``; ``GET``, ``PATCH`` (with ``if_version``) and ``DELETE
/api/v1/projects/{path}``. ``{path}`` is one percent-encoded segment
(``infra%2Fbilling``), like secret scopes. A bad path or missing parent gives
422 on ``body.path``; a duplicate, stale version or project in use gives 409.
Jobs select their project with ``spec.metadata.project``, which may now be a
nested path.

**Lookups.** Service token auth, run advance and node secret resolution read
one project with ``GetProject`` by id instead of scanning ``ListProjects``.

Alternatives considered
-----------------------

- Move and rename with rewriting of every path-keyed record: deferred; it needs
  its own design for refs, tokens and audit history.
- A materialized path column next to the name: redundant, since the path is the
  identity.

Consequences
------------

- Scope by path prefix (ADR-0060) stays correct because paths never change.
- ``node_scope`` (#207) and ``access_rules`` (#205) are later slices.
  ``retention`` is deferred to M9, as it has no semantics yet.
- A project can be removed only after its contents are removed or revoked.

References
----------

- :doc:`0042-db-secret-backend-keyring`
- :doc:`0060-service-accounts-and-api-tokens`
- ARCHITECTURE section 12.1

Known window
============

Creating a service token checks that its project exists outside the token's
transaction, so it can race with deleting that project and leave an active token
scoped to a path that no longer exists. If the path is created again, the token
covers it again. Revoking tokens before deleting a project closes the window;
the delete check only counts tokens that exist when it runs.
