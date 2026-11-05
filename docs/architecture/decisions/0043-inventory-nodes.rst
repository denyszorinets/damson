ADR-0043: Inventory nodes: schema, name grammar, interim node scope
====================================================================

:Status: Accepted
:Date: 2026-11-05

Context
-------

M4 replaces the dev-only ``--dev-nodes`` file with a real inventory
(:issue:`113`; :doc:`0034-dev-only-inline-nodes-and-secrets`). The node schema,
the name rules and the way a node reaches dispatch are expensive to change once
nodes exist, and the project tree that defines a project's node scope does not
exist before M8.

Decision
--------

Nodes table (migration 00011)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

- ``nodes``: ``id``, ``name``, ``address``, ``port``, ``transport``, ``os``,
  ``pool``, ``tags`` (JSON array), ``attributes`` (JSON object), ``credential``
  (a secret ref, never a value), ``host_key``, ``host_key_policy``,
  ``host_key_observed`` and ``host_key_observed_at``, ``source``,
  ``external_id``, ``enabled``, ``version``, ``created_at``, ``updated_at``.
- Unique on ``name`` and on ``lower(name)`` (case-only duplicates conflict),
  and unique on ``(source, external_id)`` where ``external_id`` is not empty.
  JSON is encoded in Go, not in SQL.
- ``version`` is the compare-and-swap counter. Updates and deletes address the
  row by ``id`` and ``version``, not by name, so a delete and recreate under
  the same name between a read and a write cannot be overwritten by a stale
  caller.
- ``host_key_observed*`` is written by the host key flow without changing
  ``version``.
- ``source`` is ``manual`` for nodes made through the API. ``manual`` and
  ``devfile`` are reserved for the API and the dev seeder.

Name grammar
~~~~~~~~~~~~

A node name matches ``^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$``, the same grammar as
the ``for_each`` lane key, because the name is a path segment of a lane
(:doc:`0038-for-each-lanes`). An FQDN goes in ``address``. All field rules live
in one function, ``inventory.Validate``; the API calls it, and so will sync, so
no other layer has its own rules (ARCHITECTURE 11).

Interim node scope
~~~~~~~~~~~~~~~~~~

Dispatch asks the ``inventory.Scope`` hook whether a project may use a node.
Until M8 the hook is ``AllNodes``, which matches every node, because there is no
project tree and no ``projects.node_scope`` column yet. M8 replaces it with the
CEL ``node_scope`` of ARCHITECTURE 12.1. A node outside the scope fails like a
missing node (below), so existence is not revealed.

Dispatch
~~~~~~~~

Without ``--dev-nodes`` the dispatcher reads node rows from the store at
dispatch time (``node.InventoryNodes``). With ``--dev-nodes`` the file still
serves nodes, secrets and targets as before.

.. note::

   See :doc:`0044-dev-nodes-is-a-seeder`: ``--dev-nodes`` now seeds the
   inventory and the secret store, so this split no longer exists.

- A missing node, or one outside the project's node scope, fails the step
  permanently as ``unknown node``. There is no probing.
- A disabled node fails permanently as ``node is disabled``. A node deleted
  after the run started fails the same way at dispatch.
- A database error requeues the execution.
- The node credential goes through the existing secret authorization
  (``secrets.Authorize`` with ``fromNode``): ``_inventory`` is allowed only as a
  node credential, never as a literal ref in step input
  (:doc:`0042-db-secret-backend-keyring`).

Force semantics
~~~~~~~~~~~~~~~

A node belongs to one source. Writing a node whose source is not ``manual``
through the API without ``force=true`` is a ``409`` problem of type
``urn:damson:problem:inventory-source-conflict``. With ``force=true`` the write
happens but keeps ``source``, so the owning source's next sync overwrites the
edit. ``name``, ``source`` and ``external_id`` cannot be changed by ``PATCH``.

Admin token for reads
~~~~~~~~~~~~~~~~~~~~~

All nodes routes, reads included, need the admin token
(ARCHITECTURE 12.2). Nodes carry addresses, credential refs and host keys, so
unlike the plugin list they are not public. A missing and a wrong token get the
same ``401`` text.

Pool routing deferred
~~~~~~~~~~~~~~~~~~~~~

``node.pool`` is validated and stored, but it does not route yet: ``runs_on``
still defaults to the ``default`` pool. Routing from the node pool comes with
frozen run targets (:issue:`117`).

Limits for now
~~~~~~~~~~~~~~

Inventory nodes can be used only through a literal ``node: <name>`` in step
input. ``for_each: targets`` and ``spec.targets.selector`` stay ``--dev-nodes``
only until :issue:`117`, which freezes node ids into the run and requires the
node to be in the run's targets.

.. note::

   See :doc:`0044-dev-nodes-is-a-seeder`: ``targets`` are now read from the
   inventory also for seeded dev nodes.

Alternatives Considered
-----------------------

- **Update and delete by name.** Rejected: a name can be deleted and recreated
  between read and write; the id plus ``version`` cannot.
- **A looser name grammar with FQDNs allowed.** Rejected: the name is a lane
  key and must stay a safe path segment.
- **Public reads.** Rejected: addresses and credential refs are sensitive.
- **Force replaces the source.** Rejected: it would hide that the owner's sync
  is the truth, and silently orphan nodes from their source.
- **A temporary ``node_scope`` column.** Rejected: a schema change that M8
  would redo.

Decision Criteria
-----------------

- A stale writer must never overwrite a newer row.
- One place for validation, shared by the API and sync.
- Nothing is revealed about nodes a project may not use.
- No schema we know M8 would replace.

Rationale
---------

Id plus version CAS follows the compare-and-swap rule for state changes. Coupling the name
to the lane key avoids a second naming rule for ``for_each``. The scope hook
lets dispatch call the final interface now and lets M8 change only the
implementation.

Consequences
------------

Positive
~~~~~~~~

- Nodes are managed through the API and used by ``ssh`` steps without the dev
  file.
- Stale updates fail with ``409`` instead of being lost.
- Credentials stay refs; only the dispatch path sees values.

Negative
~~~~~~~~

- Node names are limited to 63 characters, so long FQDNs cannot be names.
- Until :issue:`117`, targets and the selector work only with ``--dev-nodes``.

Risks
~~~~~

- Until M8 every project can use every node (``AllNodes``). The admin token is
  one shared credential and node writes are not audited yet.
- A forced edit of a synced node is lost at the next sync.
- Without ``if_version``, ``PATCH`` merges against the version it read and
  retries up to 3 times; a busy node can still end in ``409``.
- ``node.pool`` is accepted but ignored for routing, which may surprise
  operators until :issue:`117`.

Follow-up
~~~~~~~~~

- :issue:`115`, :issue:`116` and :issue:`118`: the remaining M4 inventory
  issues.
- :issue:`117`: frozen run targets, ``for_each: targets`` on inventory nodes
  and routing from ``node.pool``.
- M8: the project ``node_scope`` replaces ``AllNodes``.

Validation
----------

Store tests run on SQLite and Postgres. API tests cover the status codes,
per-field ``422`` locations, the force rule and the stale ``409``. Dispatch
tests cover the permanent outcomes (missing, out of scope, disabled, deleted)
and the requeue on a database error.

References
----------

- :doc:`0034-dev-only-inline-nodes-and-secrets`
- :doc:`0038-for-each-lanes`
- :doc:`0042-db-secret-backend-keyring`
- :doc:`/reference/http-api`
