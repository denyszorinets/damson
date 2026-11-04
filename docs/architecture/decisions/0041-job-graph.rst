ADR-0041: Job graph
================================================================================

:Status: Accepted
:Date: 2026-11-04

Context
-------

ARCHITECTURE 15.1 calls for an activity-diagram view of a job and of a run. The
control flow (``when``, groups, catch and finally, for_each) is defined in
``internal/engine``. If the browser derived structure from the spec it would
copy those rules and drift from them (CLAUDE.md hard rule 10). Issue #90 part 1
adds the server side; the web UI follows in later changes.

Decision
--------

Graph from the engine
~~~~~~~~~~~~~~~~~~~~~

``engine.BuildGraph(job) Graph`` is pure and deterministic: nodes depth first in
spec order, edges in the order they are found. The UI lays the graph out and
draws it; it never derives control flow from the spec.

Node kinds are ``start``, ``end``, ``step``, ``group``, ``catch`` and
``finally``. Edge kinds are ``sequence``, ``skip``, ``catch`` and ``finally``.
The start and end nodes have the ids ``$start`` and ``$end`` (``$`` cannot start
a step id). Nodes carry no ``with``: a graph holds no step input and has no
secret surface. They carry ``label``, ``parent``, ``plugin``, ``when``,
``continue_on_error``, ``retry_max_attempts``, ``timeout``, ``rethrow`` and, for
a for_each group, ``for_each`` (``expr``, ``max_parallel``, ``fail_threshold``).

Edges
~~~~~

- ``when`` other than the default ``success()`` is a node attribute plus a
  ``skip`` edge that bypasses the step, labelled with the CEL condition.
- A catch container ``<g>/catch`` and a finally container ``<g>/finally`` are
  nodes (for a for_each group ``<g>/for_each[*]/catch`` and
  ``<g>/for_each[*]/finally``). The ``catch`` edge runs from the last body child
  to the catch container (label ``on failure``). ``finally`` edges run from the
  catch container and from the last body child (label ``always``).
- A ``skip`` edge bypasses a step whose ``when`` is not ``success()`` or
  ``always()`` (an ``always()`` step is never skipped by its condition). It
  joins the step before to the step after, and the ends of a list are ``$start``
  and ``$end`` at the top of a job. Inside a group, a catch or a finally list
  there is no box boundary to draw to, so the ends are the finally list for a
  body or catch list (where control goes next) and nothing otherwise: such a
  skip edge exists only between steps that have both neighbours. No edge joins
  a node to itself or to its own container.

Node ids and lanes
~~~~~~~~~~~~~~~~~~

A node id is the instance path of its step. The graph does not grow with the
number of lanes: children of a for_each group use the template id
``<group>/for_each[*]/<id>``. ``*`` can never be a lane key, so a template never
collides with a real instance. ``engine.GraphNodeOf(path)`` maps an instance path
to ``(node, lane)``; a lane row ``g/for_each[k]`` maps to node ``g`` with lane
``k``, and outside a for_each the lane is empty.

Run graph is structure plus overlay
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

The structure depends only on the job version, so it is fetched once.
``GET /runs/{id}/graph`` returns ``{graph, steps[]}``, where each step is a
``StepEvent`` keyed by ``node`` and ``lane``. Live updates come from the
existing run event stream: ``step`` events (and the steps of the ``snapshot``)
now carry ``node`` and ``lane``. ``StepEvent.Lane`` was documented as always
empty and was never filled; it is now filled.

Endpoints
~~~~~~~~~

- ``GET /jobs/{id}/versions/{v}/graph``
- ``POST /jobs/validate`` adds ``graph`` when the spec is valid
- ``GET /runs/{id}/graph``

A stored spec that no longer parses answers a problem ``500``, which the API's
error handling logs. Versions were validated when saved, so the cause is an
engine or expression-limit change since then (or a defect), not user error.

Tests
~~~~~

Golden files ``testdata/engine/graph-NN-*.input.json`` and ``.golden.json``,
refreshed with ``-update``, plus API tests.

Alternatives Considered
-----------------------

Plain ``g/x`` ids for for_each children
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

They differ from the instance paths (``g/for_each[k]/x``) in an unmarked way and
would force the client to rebuild the mapping. Rejected: the template id keeps
the node id equal to a path with one reserved segment.

Lane-expanded graph
~~~~~~~~~~~~~~~~~~~

One copy of the body per lane. Lanes are known only at run time, and the graph
would change during a run and grow with the node count. Rejected.

Explicit decision nodes for ``when``
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

A diamond node before each conditional step. More nodes and ids that match no
step path. Rejected for a ``when`` attribute and a ``skip`` edge; the UI may
still draw a diamond from them.

Re-fetching the graph per event
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

The graph cannot change within a run, so only statuses are streamed.
Rejected as needless load.

Consequences
------------

Positive
~~~~~~~~

- Control flow lives in one place; the UI cannot drift from it.
- No secret can reach a graph, so no masking test is needed beyond checking
  that nodes have no ``with``.
- Run view updates use the stream that already exists.

Negative
~~~~~~~~

- A UI that wants a diamond must derive it from ``when`` and ``skip``.
- Per-lane state must be matched to a template node by ``node`` and ``lane``.
- The graph is rebuilt on each request (parse plus build); it is cheap and not
  cached.
