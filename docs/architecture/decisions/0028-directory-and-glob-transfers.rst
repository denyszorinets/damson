ADR-0028: Directory and glob transfers
================================================================================

:Status: Proposed
:Date: 2026-10-27

Context
-------

The ``transfer`` plugin copies a single file per step in v1. Users will want
directories and glob patterns. This affects atomicity (per file vs whole set),
verification, partial failure, outputs (lists, possibly beyond the 256 KiB
output limit) and ``move`` semantics.

Decision
--------

Not decided yet. This ADR records the question and the options so they can be
evaluated; it changes nothing until it is accepted. Planned for v1.1.

Alternatives Considered
-----------------------

Single file only
~~~~~~~~~~~~~~~~

Advantages:

- Simple atomic semantics.

Disadvantages:

- Many steps or ``for_each`` for multi-file copies.

Globs and directories with per-file atomic writes
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Covers common use.

Disadvantages:

- Partial-failure semantics and large outputs need design.

Archive-then-transfer
~~~~~~~~~~~~~~~~~~~~~

Advantages:

- One atomic object.

Disadvantages:

- Requires shell access on the source.

Decision Criteria
-----------------

- Correctness of partial failures.
- Output size limits.
- Protocol coverage (SFTP, SCP, FTP, S3).

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

- Plugin input schema change: a new major version or backward-compatible fields.

Validation
----------

Integration tests against the containerised targets for each protocol.

References
----------

- :doc:`/architecture/design` §6.3, §22 item 8
