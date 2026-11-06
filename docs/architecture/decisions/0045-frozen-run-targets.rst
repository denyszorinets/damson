ADR-0045: Runs freeze their targets in run_targets
==================================================

:Status: Accepted
:Date: 2026-11-06

Context
-------

``targets`` were read live from the inventory (:doc:`0043-inventory-nodes`,
:doc:`0044-dev-nodes-is-a-seeder`), so a node added, disabled or renamed
while a run was in progress could change the lanes of a later ``for_each``
group or make a step reach a different node (:issue:`117`).

Decision
--------

Snapshot at trigger
~~~~~~~~~~~~~~~~~~~

- When a job uses targets (``engine.UsesTargets``), ``newRun`` in the trigger
  path lists the enabled in-scope nodes, filters them with
  ``spec.targets.selector`` (``engine.SelectTargets``) and stores the result
  with the run (``NewRun.Targets``).
- The snapshot is a child table ``run_targets``: one row per target with
  ``name``, ``node_id`` and the node as a JSON map. The map is the same as
  ``targets`` elements plus ``id``, and never holds a credential or host key.
- More than 1000 matches is rejected with HTTP 422 at location
  ``spec.targets``, and no run is created.

The advancer
~~~~~~~~~~~~

- The advancer no longer reads the live inventory (``advance.Config.Targets``
  and ``advance.TargetSource`` are removed). When ``engine.UsesTargets(job)`` it
  reads ``store.ListRunTargets`` and decodes each node JSON as an element of
  ``targets``. A run without snapshot rows has empty targets.

Dispatch
~~~~~~~~

- A step's node name found in the run snapshot is resolved by its frozen
  ``node_id`` (``GetNodeByID``). A rename after the start still reaches the
  node, ``NodeInfo.Name`` stays the requested name, and a deleted node is
  ``ErrUnknownNode``. Membership is not enforced: a name outside the snapshot
  falls back to a lookup by name.
- The scope check, the enabled check and the host key policy (the interim
  fail-closed rule is superseded by :doc:`0046-tofu-host-key-recording`: only
  ``insecure`` is refused) apply to the current row. The only live data used is that row at dispatch time:
  address, credential and host key.

Alternatives Considered
-----------------------

- **A JSON column on runs.** Rejected: a child table keeps the node id
  addressable and the run row small.
- **Keep reading live.** Rejected: lanes and targets of a running run would
  change with the inventory.
- **Require strict membership of a node in the snapshot.** Rejected: a literal
  ``node: <name>`` must keep working for nodes outside the selection (pinning by
  id only fixes what a name means).

Decision Criteria
-----------------

A run must be reproducible from its start; the inventory stays the source of
truth for how to reach a node.

Rationale
---------

Freezing names and ids fixes which nodes a run covers, while resolving the row
at dispatch keeps credentials and host keys out of the snapshot and current.

Consequences
------------

Positive
~~~~~~~~

- Inventory changes never change a running run's lanes.
- A rename does not break a run.

Negative
~~~~~~~~

- A new table and a trigger-time inventory read.
- Membership is not enforced: a step may name a node outside the snapshot; the
  scope, enabled and host key checks still run on the current row. The
  maintainer accepts this narrowing of the earlier "must be in the targets" wording.
- A name in the snapshot is pinned to its node id: if that node is renamed and
  another node takes the old name, a literal ``node: <name>`` still reaches the
  original node. Host key observations (#118) must be keyed by node id, never by
  ``NodeInfo.Name``.
- The node map gains an ``id`` field, visible to selectors and expressions.
- Runs without snapshot rows (created before migration 00012, or by an older
  controller during a rolling upgrade) have empty targets; upgrade controllers
  together and drain runs that use targets.
- A run on a very large selection (over 1000) cannot be triggered.

Risks
~~~~~

- A node disabled or deleted after the start fails its steps at dispatch.
- Address, credential and host key can change between trigger and dispatch.

Follow-up
~~~~~~~~~

- :issue:`118`: host key flow for ``tofu`` (:doc:`0046-tofu-host-key-recording`).
- Not in this change: ``GET /api/v1/runs/{id}/targets``, a
  ``GET /nodes?selector=`` preview and a Targets tab in the UI.

Validation
----------

Tests on SQLite and Postgres cover the snapshot, the 422 over 1000, advancing
without the live inventory, and dispatch after a rename or delete.

References
----------

- :doc:`0038-for-each-lanes`
- :doc:`0043-inventory-nodes`
- :doc:`0044-dev-nodes-is-a-seeder`
