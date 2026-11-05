Node sync core
==============

``internal/inventory`` holds the logic behind full-state inventory sync
(ARCHITECTURE section 11). The HTTP endpoints
(``PUT /inventory/sources/{source}/nodes`` and ``:batch``) come with #116
part 2; ``--dev-nodes`` is its first client, as source ``devfile`` (ADR-0044).

Flow
----

``inventory.Sync(ctx, store, source, desired, prune, dryRun, now)``:

1. Validates and normalizes every desired node with ``inventory.Validate``,
   with ``source`` forced to the sync source. It rejects duplicate names
   (case-insensitive) and duplicate external ids with ``ErrBadDesired``.
2. Reads the nodes the source owns.
3. Plans with the pure function ``inventory.PlanSync``.
4. Applies the plan with ``NodeStore.ApplyNodeDiff`` (see :doc:`store`).
   If a concurrent writer moved a node (``ErrStale``), it re-plans, up to 3
   times.

In dry run nothing is written; the diff is returned.

Matching
--------

- A desired node matches an own node by ``external_id``, else by name. An
  own node without an external id is adopted by name.
- A changed name for the same external id is a rename.
- With ``prune``, own nodes that match nothing are deleted.
- A node whose normalized fields are equal produces no write and no version
  bump. The observed host key and the version are not compared.

Conflicts
---------

A desired name held by a node of another source, or by an own node that is
not pruned, is reported in ``Diff.Conflicts``. ``Sync`` then returns
``ErrSyncConflict`` having written nothing, also in dry run. Names are compared
ignoring case, as the unique index does. The same rule covers the target name
of a rename: it is free only if no other source holds it and no own node holds
it, unless that node is the one being renamed (a case-only rename is fine) or
is pruned in the same call. So a swap of two own names, or a chain of renames
in the wrong order, is reported as a conflict instead of failing late in the
store; split it into two syncs.

An own node is claimed by one desired node only: external ids are matched
first, names second among the nodes still free. The ``manual`` source belongs
to the nodes API and cannot be synced. A replay with nothing to write opens no
transaction.

Concurrent syncs of one source take row locks in id order; a Postgres deadlock
or serialization failure is treated like a moved node and the sync re-plans.

Result
------

``Diff`` has the node names in ``Created``, ``Updated``, ``Deleted``,
``Unchanged`` and ``Conflicts``.
