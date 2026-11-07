ADR-0055: Inventory target mode
===============================

:Status: Accepted
:Date: 2026-11-07

Issue :issue:`181`. Amends :doc:`0048-plugins-that-target-several-nodes` and
:doc:`0046-tofu-host-key-recording`.

Context
-------

The ``ansible`` plugin (ARCHITECTURE 6.4) runs one play over up to 1000 nodes in
one step. ``TargetMode`` has had an ``inventory`` value since SDK 0.1, but
dispatch only implements ``single_node`` and ``nodes`` (at most 8 fixed
pointers). Nothing resolved many nodes, checked their scope or delivered their
credentials.

Decision
--------

**Manifest.** ``Manifest.NodeListPath`` (``node_list_path``, ``omitempty``) is
one RFC 6901 pointer to an array of node names. It is required with
``inventory`` and forbidden with every other mode; ``NodePaths`` stays forbidden
with ``inventory``. ``pluginsdk.MaxNodeList = 1000`` equals the cap on
``spec.targets``. ``Manifest.NodeNames`` returns the names deduplicated and
sorted. The pointer must be present and name an array of non-empty strings; an
empty array is allowed (the plugin succeeds with no hosts). More than 1000 names
is a permanent dispatch error. SDK 0.3.0 to 0.4.0, additive; ``omitempty`` keeps
the checksums of existing manifests.

**Input carries names, not nodes.** Ansible's ``hosts`` is a list of names,
normally ``${{ targets.map(t, t.name) }}`` or a literal list. Whole ``targets``
objects would bloat the stored (audited, replayable) input and be a second,
frozen copy of address and attributes beside the dispatch-time ``NodeInfo``,
which stays authoritative. ``limit``, ``tags`` and ``skip_tags`` are plain
ansible options; they can only narrow the generated inventory.

**Dispatch.**

- Names are resolved in sorted order through the existing node source: scope,
  enabled state and host key policy apply to each, and every node credential is
  authorized as a node credential (the ``_inventory`` scope is allowed for it),
  as before, and added to the bundle.
  The bundle is keyed by ref, so a shared credential is sent once; the limit of
  64 distinct refs is unchanged.
- A name that is unknown, out of scope, disabled or whose policy is ``insecure``
  fails the **whole step** permanently, naming the first such node in sorted
  order. It is not skipped: skipping would silently change what the play runs
  on. Users exclude nodes with the selector.
- The ``step.execute`` request is encoded before sending; if it is larger than
  ``MaxFrameSize`` minus 64 KiB the step fails permanently instead of entering
  the ambiguous-outcome path.
- Pool routing (ADR-0052) already works on the node list: one pool, or set
  ``runs_on.pool``.

**NodeInfo.Tags.** ``NodeInfo`` gains optional ``tags`` (the inventory tags) so
the plugin can build groups. The protocol is unchanged: msgpack decoding with the
``json`` tag skips unknown fields, so an agent at N-1 ignores it (pinned by a
codec test).

**Older controllers.** A controller at SDK 0.3.0 accepts the mode but resolves
nothing. The plugin must fail closed: a listed host without a ``nodes`` entry is
a ``validation`` error. No credential is sent, so nothing leaks.

**Host keys.** The fence of ADR-0048 is unchanged: a report is accepted only for
a node in ``NodeNames`` of the step input. The ansible plugin sets a per-host
``HostKeyAlias`` and ``StrictHostKeyChecking`` (``yes`` for strict and for tofu
with a key, ``accept-new`` for tofu without one), forces
``ANSIBLE_HOST_KEY_CHECKING=True`` in the environment, and reports newly seen
keys of tofu nodes after the run, also after a failure or cancel.

Alternatives Considered
-----------------------

- *Whole ``targets`` objects in the input.* Rejected: size, duplicate truth.
- *Dispatch reads ``run_targets`` and filters by ``limit``.* Rejected:
  ``run_targets`` exist only when the job uses targets, and a step inside a
  ``for_each`` could not pick a subset.
- *A new mode value.* Rejected: ``inventory`` is already in the enum and the wire
  format.
- *Skip unavailable nodes.* Rejected: silent change of scope.

Consequences
------------

- Dispatch of a large inventory step costs about four store queries per name,
  run twice (pool routing and resolution). A batch node source is a follow-up
  (issue :issue:`187`); 1000 nodes are correct today, only slower.
- Accepted risk: play variables (``ansible_ssh_common_args``,
  ``ansible_host_key_checking``) outrank inventory host variables, so a playbook
  can turn host key checking off. The check guards against network
  man-in-the-middle, not against a hostile playbook, which can already read the
  credential files with ``delegate_to: localhost``. Playbooks are trusted code.
- During a rolling upgrade a controller at SDK 0.3.0 decodes an inventory
  manifest without ``node_list_path``, so its checksum differs from the new
  controller's for the same ``name@version`` and a schema conflict can be
  flagged until all controllers run the same version. No inventory plugin
  shipped before this change.
- Credentials of up to 1000 nodes are in agent memory for the run; the plugin
  writes them only to 0600 files that it removes on every exit path.

Validation
----------

``pluginsdk`` tests cover ``NodeNames`` and manifest validation; the dispatch
end-to-end test covers delivery, duplicates, an empty list, unknown and disabled
hosts and the cap on both databases; the host key fence has an inventory case;
the codec test pins ``NodeInfo.tags`` against an N-1 decoder; the ``plugintest``
contract suite checks an inventory plugin's cases against ``NodeNames`` and, with
a ``nodes_fail_closed`` check, that it refuses a listed host that was not
delivered. ``examples/plugins/hello`` has mode ``none`` and needs no change.

References
----------

- :doc:`0048-plugins-that-target-several-nodes`
- :doc:`0046-tofu-host-key-recording`
- ARCHITECTURE sections 5.5, 6.4, 7.2
