ADR-0066: Project node scope
============================

:Status: Accepted
:Date: 2026-11-08

Issue :issue:`207`. Completes the "Interim node scope" of
:doc:`0043-inventory-nodes`. The earlier text stays as written; where it
differs, this ADR wins.

Context
-------

ADR-0043 left dispatch asking the ``inventory.Scope`` hook, with ``AllNodes``
allowing every node until the project tree (:doc:`0062-project-tree`) existed.
The tree exists, so a project can now limit which inventory nodes its jobs use.

Decision
--------

**Column.** ``projects.node_scope TEXT NOT NULL DEFAULT ''`` (migration 00021,
both dialects). It is a CEL expression with one variable, ``node``: the plain
node map that selectors use (``id``, ``name``, ``address``, ``tags`` (list),
``attributes`` (map), ``pool``, ``os``, ``transport``). It never holds a
credential, host key or port. An empty scope allows every node. Examples:
``'prod' in node.tags``, ``node.attributes.env == 'prod' && node.os == 'linux'``,
``node.name.startsWith('web-')``, ``node.pool == 'build'``.

**Inheritance.** The effective scope is the AND of the project's own scope and
the scope of every ancestor, so a child can only narrow.

**Fail closed.** A scope that does not compile, or fails to evaluate for a node
(for example ``node.attributes.region`` is missing), allows no node. The
failure is logged without node data.

**Limits.** At most 2048 bytes. The expression is compile-checked on write
(``422`` at ``body.node_scope``) and cost-limited like other CEL.

**Where it applies.** ``inventory.ProjectScope`` replaces ``inventory.AllNodes``
(which stays for tests and the dev seeder). It is used in two places:

- Dispatch (``InventoryNodes.admit``): a node outside the scope fails the step
  permanently as ``unknown node``, the same as a missing node, so existence is
  not revealed.
- The frozen run targets (``InventoryTargets``): ``targets`` holds only enabled,
  in-scope nodes.

**Caching.** The project chain is cached per project for 10 seconds per
controller (injected clock). A changed scope applies on every controller within
about 10 seconds; tightening one takes up to 10 seconds to reach a controller
that still has the old chain cached. Runs already started keep their frozen
targets, but dispatch re-checks the scope for every step.

**API.** ``GET``/``PUT /api/v1/projects/{path}/node-scope`` need the ``admin``
role on the project (an instance admin or a project admin). The body is
``{node_scope, if_version}`` and the response ``{node_scope, version}``. The
write is a compare-and-swap on the project version, shared with the description
and the access rules (``409`` when stale).

**Audit.** ``project.node_scope.update`` (target type ``project``;
``project_path`` is the project; details ``path`` and ``set``, never the
expression).

Consequences
------------

- The nodes inventory API (list, get, create nodes) stays instance-admin only
  and shows every node. Node scope limits what a project's jobs can use, not
  what the inventory API shows.
- A bad scope stops a project's jobs from using any node rather than all of
  them. The failure shows as ``unknown node``, which can look like a missing
  node; the controller log names the project.
- Tightening a scope is not instant across controllers (up to 10 seconds).

Alternatives considered
-----------------------

- **A scope that can widen what an ancestor allows (OR).** Rejected: a child
  could escape its parent's limit.
- **Fail open on an error.** Rejected: a typo would expose every node.
- **A distinct "out of scope" error.** Rejected: it would reveal that the node
  exists.
