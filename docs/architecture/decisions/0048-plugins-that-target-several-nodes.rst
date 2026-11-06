ADR-0048: Plugins that target several nodes
===========================================

:Status: Accepted
:Date: 2026-11-06

Context
-------

``TargetMode`` knows ``none``, ``single_node`` (one node at ``input.node``) and
``inventory`` (a selector). The ``transfer`` plugin (ARCHITECTURE 6.3,
:issue:`144`) has a source and a destination that can each be a node or an S3
endpoint, so one step can name two nodes at different places in its input.
Dispatch must resolve, authorize and give credentials for both, and the host key
fence of :doc:`0046-tofu-host-key-recording` must accept reports for both.

Decision
--------

Manifest
~~~~~~~~

- New ``pluginsdk.TargetNodes TargetMode = "nodes"`` and
  ``Manifest.NodePaths []string`` (``json:"node_paths,omitempty"``).
- ``NodePaths`` holds 1 to 8 unique RFC 6901 JSON pointers into the step input,
  each naming a node (for example ``/source/node/name`` and
  ``/destination/node/name``). It is required with mode ``nodes`` and forbidden
  with any other mode.
- ``omitempty`` keeps the checksum of every existing manifest unchanged.
- SDK ``0.2.0`` to ``0.3.0``, additive.
- ``Manifest.NodeNames(input)`` returns the node names in path order, without
  duplicates. ``single_node`` behaves as the path ``/node`` and is required.
  For ``nodes`` a missing pointer is skipped (an S3 endpoint has no node); a
  present value that is not a string or is empty is a permanent dispatch error
  that names the pointer.

Dispatch
~~~~~~~~

- ``internal/controller/dispatch/targets.go`` resolves every named node through
  the node source and adds each node credential to the secret bundle as an
  ``_inventory`` node credential. References are authorized as before.
- ``step.execute.nodes`` is already a map keyed by node name, so no protocol
  message changes and protocol N-1 is unaffected.
- An older controller that sees a manifest with mode ``nodes`` fails manifest
  validation and skips only that manifest; the agent's hello is not refused.

Host key fence
~~~~~~~~~~~~~~

``node.hostkey_observed`` is accepted only if the reported node is one of the
step's node names (``NodeNames`` of the resolved manifest and the step input),
for ``single_node`` and ``nodes``. This amends :doc:`0046-tofu-host-key-recording`,
which required the step's single ``input.node``. A report for an S3 endpoint or
a third node is dropped.

API and contract suite
~~~~~~~~~~~~~~~~~~~~~~

The plugin ``target_mode`` enum is now ``none``, ``single_node``, ``inventory``,
``nodes``. The ``plugintest`` contract suite has a ``nodes`` check: each case's
``Nodes`` must equal the manifest's ``NodeNames`` of the case input.

Transfer partial file name
~~~~~~~~~~~~~~~~~~~~~~~~~~

This is the decision for the ``transfer`` plugin, which lands in a later change.

- Each attempt writes to ``<path>.damson-partial-<8 random hex>`` and renames
  on success. This replaces the fixed ``<path>.damson-partial`` of ARCHITECTURE 6.3.
- A fixed suffix is not safe: a lost attempt may still run on a disconnected
  agent while its retry starts, and both would write the same file.
- The partial file is deleted on a best-effort basis on failure or cancel.
- The ``transfer`` manifest has ``Idempotent=false``.

Alternatives Considered
-----------------------

- A generic collection of strings under keys named ``node``: guesses what is a
  node and misfires on user data (for example an ``env`` map with a ``node``
  key).
- The plugin resolves inventory itself: breaks the authorization of
  ARCHITECTURE 9.1 and the rule that secrets exist only in the dispatch path.

Consequences
------------

- Plugins with several node endpoints declare them in the manifest; dispatch
  stays the only place that resolves nodes and credentials.
- Controllers older than SDK 0.3.0 skip such plugins instead of mis-dispatching.
- Plugin authors must keep ``NodePaths`` in sync with the input struct; the
  contract suite checks it.
