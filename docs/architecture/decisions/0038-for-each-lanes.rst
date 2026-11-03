ADR-0038: for_each lanes
================================================================================

:Status: Accepted
:Date: 2026-11-03

Context
-------

A group with ``for_each`` runs its steps once per item of a list, in
independent lanes (ARCHITECTURE 5.3 rule 6, issue #87). Groups, scoping and
nested paths are in :doc:`0037-groups-scoping-and-status`. Lanes need a stable
identity, a list that does not change while the run goes on, bounded
parallelism, a failure threshold, and rules for how catch, finally, cancel and
the job timeout apply to every lane.

Decision
--------

The list
~~~~~~~~

- ``for_each`` is a CEL expression (not a ``${{ }}`` template) that must
  evaluate to a list. It is evaluated **once**, in the enclosing scope, when the
  group starts, and frozen as ``{"lanes": [{"key", "item"}, ...]}`` in the
  group row's ``input``. It is never re-evaluated, so the lanes of a run do not
  change. It cannot call status functions.
- The lane key of an item is its ``name`` when it is an object with a string
  ``name``, the item itself when it is a string, else its index. A key must match
  ``^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`` and be unique; an invalid or duplicate
  key, an expression that fails or is not a list, or more than 1000 items
  **fails the group** before any lane starts (no catch or finally runs for it).
- The result is limited by ``Limits.MaxResultBytes`` (64 KiB by default).
- An empty list makes the group succeed with zero lanes.

Lanes
~~~~~

- The group has one aggregate row at its path, and each lane is a group row at
  ``<group>/for_each[<key>]``. A lane is a **full instance of the group**: its
  ``steps``, ``catch``, ``finally`` and ``rethrow`` apply inside it, per lane,
  with the lane's ``item`` bound. Lane paths for its steps are
  ``<group>/for_each[<key>]/<id>``, ``.../catch/<id>`` and ``.../finally/<id>``.
  A step id cannot contain ``[`` or ``_``, so a lane segment never collides with
  an id or a marker.
- ``item`` is the lane's item (any JSON value). ``node`` is the item when it is
  an object (for ``targets``, a node), otherwise empty. Both are inherited by
  everything nested in the lane. ``targets`` is the node list ``for_each`` can
  iterate: before the inventory (M4) the dev nodes of ADR-0034 filtered by
  ``spec.targets.selector``, empty without a dev file.
- ``steps.<id>`` inside a lane is lexical as in a group: the lane's own earlier
  steps first, then the enclosing lists; lanes do not see each other. Outside, a
  for_each group's value is ``{status, outputs, error, handled, started_at,
  finished_at, lanes}`` with ``lanes[<key>]`` = ``{key, item, status, error,
  handled, started_at, finished_at, steps}``.
- ``for_each`` groups do not nest in v1 (not under another ``for_each`` group at
  any depth), to keep the fan-out bounded.

Scheduling
~~~~~~~~~~

- Lanes open in list order. ``max_parallel`` (1 to 1000; unset means all) limits
  the lanes open at once. Unlike a step list, the walk does not stop at the first
  unfinished lane: every open lane progresses in the same ``Decide``.
- ``fail_threshold`` (1 to 1000; unset means never) is the number of failed lanes
  after which the lanes that have not started are cancelled ("fail_threshold
  reached"). Running lanes finish. A lane is failed when its group failed; a
  lane whose failure its catch handled succeeded and does not count, and a
  rethrown one counts. A lane that has just failed but whose last attempt is not
  settled yet holds new lanes back until the next ``Decide``, so the threshold
  sees it before another lane opens.
- During a cancel or the job timeout the lanes that have not started are
  cancelled ("run cancelled" / "job timed out"), unless the group was admitted
  after the cancel (``always()``); the attempts of running lanes are cancelled as
  for any step, and a lane's finally list is protected during the finally grace
  (ADR-0037). In a finally list, lanes that have not started when the grace ends
  are cancelled.

Group status
~~~~~~~~~~~~

The aggregate is finished when every lane is terminal: ``failed`` if any lane
failed, with the **first failed lane's error in list order**, unchanged (its
``path`` is the leaf step, such as ``per-node/for_each[b]/install/unpack``);
else ``cancelled`` if any lane was cancelled; else ``succeeded``.

Store and API
~~~~~~~~~~~~~

No migration, no protocol change. Lane rows are ``kind = 'group'`` rows (started,
skipped and finished with the group operations of ADR-0037); the aggregate's
``input`` holds the lane list. The API step list stays flat in creation order;
clients nest by path.

Alternatives Considered
-----------------------

One group row and lane-qualified steps
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

No lane rows, so no lane status, catch or finally per lane. Rejected: the
per-node rollback of the ARCHITECTURE 5.1 example needs a lane that can fail and
be handled on its own.

Re-evaluating the list on every ``Decide``
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

The list could change between advances (an input, a step's output) and lanes
would appear or vanish mid-run. Rejected for the frozen list.

Consequences
------------

Positive
~~~~~~~~

- Lane identity and the list are stable across controller crashes and failover:
  everything is in the database and every change is one version-checked
  transaction.
- Per-lane catch and finally give the rolling-update rollback pattern.

Negative
~~~~~~~~

- Memory and rows grow with lanes times steps (at most 1000 lanes); outputs of
  every lane step are loaded into the expression context.
- A cleanup that must run once after all lanes is a following ``always()`` step
  or a wrapping group, not a ``finally`` of the ``for_each`` group.
- During a rolling upgrade a controller without this change rejects ``for_each``
  at validation and fails the run; finish the rollout of every controller before
  authoring jobs that use it.
