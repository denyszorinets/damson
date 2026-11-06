ADR-0046: The controller records the first host key of tofu nodes
=================================================================

:Status: Accepted (amended by :doc:`0048-plugins-that-target-several-nodes`)
:Date: 2026-11-06

Context
-------

A node with host key policy ``tofu`` has no pinned ``host_key``. The ``ssh``
plugin reports the key it saw through ``node.hostkey_observed``, but until now
the controller stored nothing, so dispatch refused ``tofu`` without a pin as an
interim fail-closed rule (:doc:`0044-dev-nodes-is-a-seeder`,
:doc:`0045-frozen-run-targets`; :issue:`118`).

Decision
--------

Recording
~~~~~~~~~

- The controller handles ``node.hostkey_observed`` in
  ``internal/controller/dispatch/hostkeys.go``, fenced like ``step.complete``:
  the attempt must exist, belong to the reporting agent and be unfinished, and
  the step's ``input.node`` must equal the reported node.
- The key is parsed; its type and SHA256 fingerprint are recomputed and a
  mismatch is dropped. It is stored in canonical ``authorized_keys`` form
  without comment. Drops are silent (INFO log).
- The node is resolved like dispatch: the frozen run target id, else by name.
- ``NodeStore.ObserveNodeHostKey(id, key, now)`` is a single ``UPDATE ... WHERE
  host_key_policy = 'tofu' AND host_key = '' AND host_key_observed = ''``. The
  first key wins, nothing is overwritten, ``version`` does not change and a
  replay is a no-op. ``ClearNodeHostKeyObserved(id)`` clears it.

Dispatch
~~~~~~~~

- ``strict``: the pinned ``host_key`` (empty fails at connect, as before).
- ``tofu``: the pinned ``host_key``, else the observed key, else no key. With
  no key the agent accepts the first key it sees and reports it; the ``ssh``
  plugin enforces any key it is given.
- ``insecure``: still refused by the controller as a permanent error
  ``host key policy not available``. The agent's ``allow_insecure_host_keys``
  stays a second gate.

Operator control
~~~~~~~~~~~~~~~~

- ``DELETE /api/v1/nodes/{name}/host-key`` (admin token; ``204``, ``404``)
  forgets the observed key so the next connection observes again. It works for
  synced nodes without ``force`` because the observed key is not source-owned.
- ``PATCH`` ``host_key`` pins a key, which wins over the observed one.
- The node detail page has a Forget button next to the observed fingerprint.

Alternatives Considered
-----------------------

- Let the agent write the key directly: breaks the rule that agents only
  report and the controller applies with fencing.
- Overwrite the observed key when it changes: a man-in-the-middle after the
  first connection would become trusted automatically.
- Refuse ``tofu`` entirely: leaves the policy unusable.

Consequences
------------

- Only ``insecure`` is refused at dispatch; the interim fail-closed rule is
  superseded.
- Known limit: a ``tofu`` node whose key legitimately rotates must be
  forgotten by an operator. A changed key is refused by the plugin and never
  replaced automatically.
- The first connection of a ``tofu`` node is unauthenticated by design; pin the
  key (``strict``) where that is not acceptable.
- Recording is fenced in the same statement that writes (the attempt must be an
  unfinished one of the reporting agent) and goes through the dispatch checks
  (scope, enabled, single-node plugin). A report that loses to a different
  first key is logged as a warning with both fingerprints: it means two first
  connections saw different keys.
- Reports are applied by one bounded worker; a full queue drops them (the next
  run observes again).
- A compromised agent can still lie about the key of a node its step
  legitimately targets, and attempts dispatched before the first report lands
  accept any key. Both are inherent to agent-observed TOFU.
- A report for a name that is not in the run's frozen targets is resolved by
  name again at report time, so a rename in between can attach the key to the
  wrong node (it fails closed: later connections are refused).
- Editing a node's address, port or policy keeps its observed key; forget it
  after re-addressing a node.
