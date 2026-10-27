ADR-0024: YAML as an accepted input format
================================================================================

:Status: Proposed
:Date: 2026-10-27

Context
-------

JSON is the canonical job spec format (:doc:`0013-json-job-specs-as-versioned-data`).
Many operators prefer writing YAML. The CLI could accept YAML for
``damson apply`` and convert it to JSON before upload, keeping the server and
database JSON-only.

Decision
--------

Not decided yet. This ADR records the question and the options so they can be
evaluated; it changes nothing until it is accepted. Target: before M9 (``apply``).

Alternatives Considered
-----------------------

JSON only
~~~~~~~~~

Advantages:

- One format everywhere; no conversion ambiguity.

Disadvantages:

- Verbose to hand-write; no comments.

YAML accepted by the CLI, converted to JSON
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Comfortable authoring; server unchanged.

Disadvantages:

- YAML typing pitfalls (``no`` → false, version numbers as floats).
- Round-trip from ``export`` loses comments.

YAML accepted by the server API
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Every client and the UI must handle two formats.

Decision Criteria
-----------------

- Author ergonomics.
- Determinism of conversion (checksums must be stable).
- Dependency (a YAML library, license check).

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

- If accepted, choose a YAML library and record its license (ADR-0021).

Validation
----------

Conversion golden tests: YAML fixtures convert to the same JSON (and checksum) as their JSON twins.

References
----------

- :doc:`/architecture/design` §5, §13, §22 item 3
