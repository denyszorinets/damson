ADR-0020: Server-built job graph with an in-house layout
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Jobs and runs are shown as activity diagrams in the job detail, the editor
preview and the live run view. Control-flow semantics must not be implemented
twice (Go engine and TypeScript UI). Layout must stay stable between runs, and
dependencies must be GPL-compatible.

Decision
--------

- ``engine.BuildGraph(spec) Graph`` produces nodes and edges whose ids are step
  paths, the same paths StepInstances use. The API serves it at
  ``…/versions/{v}/graph``, ``POST …/validate`` and ``runs/{id}/graph``.
- The UI never re-derives control flow from the spec; it lays out and renders.
- Rendering: ``@xyflow/svelte`` (Svelte Flow, MIT) with custom node components.
- Layout: a small deterministic recursive box layout (~150 LOC): lists stack
  vertically, groups wrap children, ``catch`` and ``finally`` are columns to the
  right. Above 20 lanes, fan-outs collapse into a summary.
- Live status arrives on the run SSE stream as ``{path, lane, status, attempt}``.

Alternatives Considered
-----------------------

ELK.js
~~~~~~

Disadvantages:

- EPL-2.0 license complicates GPL distribution.

dagre
~~~~~

Disadvantages:

- Handles nested groups poorly.

Graph derived in the browser
~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Duplicates §5.3 semantics in TypeScript; drift.

Decision Criteria
-----------------

Single source of semantics, license, layout stability, code size.

Rationale
---------

Specs are structured (nested sequential lists, no arbitrary edges), so a
general graph layout engine is unnecessary.

Consequences
------------

Positive
~~~~~~~~

- One semantic model; stable, deterministic layouts.

Negative
~~~~~~~~

- Layout code to maintain; arbitrary DAGs would need rework (see
  :doc:`0026-parallel-groups`).

Risks
~~~~~

- None significant.

Follow-up
~~~~~~~~~

- M3: ``BuildGraph`` and the Svelte Flow view with live status.

Validation
----------

Golden tests for ``BuildGraph`` over engine fixtures; snapshot tests for the
layout function.

References
----------

- :doc:`/architecture/design` §15.1
- :doc:`0009-pure-deterministic-engine`
