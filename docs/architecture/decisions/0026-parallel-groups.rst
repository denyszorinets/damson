ADR-0026: Parallel groups beyond node lanes
================================================================================

:Status: Proposed
:Date: 2026-10-27

Context
-------

In v1 the only parallelism inside a job is ``for_each`` lanes. Jobs sometimes
need heterogeneous parallel branches (build image A and B at once, then deploy).
A ``type: parallel`` group would change engine semantics (§5.3), the graph
model and layout, and failure handling (fail-fast vs wait-all).

Decision
--------

Not decided yet. This ADR records the question and the options so they can be
evaluated; it changes nothing until it is accepted. Post-v1; requires an engine semantics design in plan mode.

Alternatives Considered
-----------------------

No parallel groups
~~~~~~~~~~~~~~~~~~

Advantages:

- Simplest engine and graph.

Disadvantages:

- Users chain separate jobs or abuse ``for_each``.

``type: parallel`` group with named branches
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Structured, still no arbitrary edges; layout stays box-based.

Disadvantages:

- New semantics for ``when`` and ``steps.<id>`` scoping across branches.

Arbitrary DAG (``needs:``)
~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Maximum flexibility.

Disadvantages:

- Requires a general graph layout; much harder to reason about and test.

Decision Criteria
-----------------

- Fit with the pure engine (ADR-0009) and the box layout (ADR-0020).
- User demand.
- Test surface.

Rationale
---------

Pending the decision.

Consequences
------------

Positive
~~~~~~~~

- To be recorded with the decision.

Negative
~~~~~~~~

- To be recorded with the decision.

Risks
~~~~~

- Deciding late may force changes to already-shipped behaviour; see Context.

Follow-up
~~~~~~~~~

- Any change updates ARCHITECTURE §5.3 and the spec schema.

Validation
----------

Golden tests for every new rule in ``testdata/engine/``; graph snapshot tests.

References
----------

- :doc:`/architecture/design` §1 non-goals, §5.3, §22 item 5
- :doc:`0009-pure-deterministic-engine`
