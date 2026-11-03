ADR-0037: Groups: scoping, nested paths and status
================================================================================

:Status: Accepted
:Date: 2026-11-03

Context
-------

ARCHITECTURE 5.2 and 5.3 describe ``type: "group"`` with ``steps``, ``catch``,
``finally`` and ``rethrow`` (issue #86); ``for_each`` lanes (#87) build on it.
The engine walked one flat list. Groups need nested instances, scoped
expression variables, a derived status for the group itself, and room for the
catch and finally lists and for lanes.

This ADR records the decisions of the first change (groups with ``steps``);
the same ADR is extended when ``catch``, ``finally`` and ``rethrow`` land.

Decision
--------

Paths
~~~~~

- A step instance's path is the ids from the top of the job down to the step,
  joined by ``/``. A top-level step's path is its id, so rows and retry jitter
  written before groups keep theirs. A body child is ``<group>/<id>``; the
  children of a catch or finally list will be ``<group>/catch/<id>`` and
  ``<group>/finally/<id>``.
- Inside a group the ids ``catch`` and ``finally`` are reserved (validation
  rejects them), so a marker segment can never be a step id. A top-level step
  may still use them, as jobs saved before groups existed may: a marker is only
  ambiguous right after a group segment. A group's ``steps``, ``catch`` and
  ``finally`` will form one id namespace when the catch and finally lists land.
- Groups nest at most 8 deep.

Engine
~~~~~~

- ``Decide`` walks scopes recursively, keyed by path (``scope.go``). A scope is a
  step list with its path prefix and its parent. A step that is not finished
  ends the walk, so nothing after it starts (rule 1).
- A group is a ``step_instances`` row with ``kind = 'group'``, created by
  ``CreateStepInstance`` with ``Kind: "group"`` (no plugin, input or
  execution) and finished by the new action ``CompleteGroup``. The group row is
  created, and its first step with it, in the same ``Decide``; a group whose
  ``when`` is false or whose evaluation fails gets no children.
- A group is finished when its body is done: ``failed`` if a body step failed
  and did not ``continue_on_error``, with that step's error (carrying the path
  of the plugin step that raised it); else ``cancelled`` if a step was
  cancelled; else ``succeeded``. At group level a real failure beats a cancel,
  as a failure still triggers the ``failure()`` handlers of the enclosing list.
- ``when`` and ``continue_on_error`` apply to a group; ``retry``, ``timeout``,
  ``runs_on``, ``plugin`` and ``with`` do not (a group has no attempts).

Expressions
~~~~~~~~~~~

- ``steps.<id>`` is lexical: a step sees the earlier siblings of its own list
  and, further out, those of every enclosing list; an inner id shadows an outer
  one. ``steps.<group>`` has ``status``, ``outputs``, ``error``, ``handled``,
  ``started_at``, ``finished_at`` and ``steps``, the values of its body steps.
- A group that is admitted after a cancel (its ``when`` calls ``always()`` or
  ``cancelled()``; the run is cancelled by a user or by the job timeout) is
  created at or after the cancel, so the run-level cancel does not count against
  ``success()`` in its body: its steps run like the ``always()`` steps created
  after a cancel do. Failures inside it still count, and no step is retried
  while the run is being cancelled.
- The status functions are local to a list: ``success()`` and ``failure()``
  look only at the earlier steps of the same list; ``cancelled()`` is the
  run-level cancel.

Store and API
~~~~~~~~~~~~~

- No migration. A created group starts ``running`` with ``started_at`` and is
  finished by ``Advance.FinishGroups`` (a compare-and-swap that keeps
  ``started_at``); a group cannot take an attempt or a settle.
- The API keeps a flat step list in creation order; ``kind`` is ``group`` for a
  group and clients nest by path, which is also the graph node id.

Alternatives Considered
-----------------------

Marker segments that cannot be ids (``g/[catch]/x``)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

No reserved ids, but harder to read in the API, logs and graph. Rejected.

Status functions over the whole group
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

``failure()`` in a ``finally`` step would see the body's failure. Less
predictable once groups nest and lanes arrive. Rejected for the list-local rule;
authors use ``steps.<id>.status`` or ``error``.

Consequences
------------

Positive
~~~~~~~~

- One walk handles every nesting depth, and the same shape takes lanes.
- Existing rows, paths and goldens are unchanged.

Negative
~~~~~~~~

- Two ids are reserved inside groups.
- The flat API list needs clients to nest by path.
- During a rolling upgrade a controller without this change cannot parse a job
  with ``type: "group"`` and fails the run, and one that reads group rows fails
  on them. Finish the rollout before authoring group jobs.
