ADR-0052: Route steps by node pool at dispatch
==============================================

:Status: Accepted
:Date: 2026-11-06

Supersedes in part :doc:`0043-inventory-nodes` (the "Pool routing deferred"
section). Fulfils :doc:`0007-stateless-worker-agents-reaching-targets-remotely`
("selects a pool from ``runs_on`` or the target node's pool"). Issue
:issue:`140`.

Context
-------

``node.pool`` was stored but did not route: a step without ``runs_on.pool``
always went to the ``default`` pool. Operators group agents by network
segment, so a step that targets a node in ``dc1`` must run on a ``dc1`` agent.

The advancer cannot decide this. It does not read the inventory, literal, CEL
and ``for_each`` node names are not in ``run_targets``, and an unknown node
should fail fast rather than wait in a queue.

Decision
--------

- Precedence: ``runs_on.pool``, then the pool of the target node, then
  ``default``. A ``runs_on`` with only ``labels`` still routes by node.
- The advancer sets the execution pool to ``runs_on.pool`` when given, else to
  the reserved marker ``@node``. ``@`` is not a legal pool name, no agent has
  that pool, and the claim of every agent includes it.
- At dispatch, a claimed ``@node`` execution is resolved without reading any
  secret:

  #. Load the step and resolve the plugin in any pool. With no match the
     execution is incapable: requeued after 5 s, and the row stays ``@node``.
  #. ``Manifest.NodeNames(input)`` gives the node names. Each is looked up
     through ``NodeSource`` (``PoolSource.NodePool``) with the same scope,
     enabled and host key policy checks as ``Node``. An unknown, disabled or
     ``insecure`` node fails the step permanently.
  #. Nodes with an empty pool add no constraint. One distinct pool selects it,
     none selects ``default``. Two or more distinct pools fail the step
     permanently: ``step targets nodes in pools dc1, dc2; set runs_on.pool``.

- If the target pool is the claiming agent's pool, the normal send continues.
  Otherwise ``Store.RouteExecution`` moves the execution: a fenced
  compare-and-swap (dispatched under this ``attempt_id``, pool still
  ``@node``, not flagged for cancel) that sets the pool, state ``queued`` and
  clears agent, controller and lease. ``ErrStale`` cancels the undelivered
  attempt.
- Each retry is a new execution and is routed again against the node's current
  row.
- A pool without a connected agent leaves the execution queued, as an unknown
  ``runs_on.pool`` does today. The job timeout and cancel still apply.
- The fallback is ``default``, not "any pool": local steps such as buildah or
  helm must not land on build pools. "Any pool" can be added later by routing
  node-less rows to the claiming agent's pool.
- No job spec, protocol, DDL or SDK change (``NodeInfo`` does not carry the
  pool). One new value of ``executions.pool`` and one new query
  (``RouteExecutionCAS``).

Consequences
------------

- A routed step costs one extra claim and requeue when the first claimer is in
  another pool.
- A step that names nodes in several pools must set ``runs_on.pool``.
- A bad node name fails at dispatch instead of at the plugin.
