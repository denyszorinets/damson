ADR-0029: Multi-arch image builds and image signing
================================================================================

:Status: Proposed
:Date: 2026-10-27

Context
-------

The ``buildah`` plugin builds a single platform in v1. Multi-arch builds need
qemu emulation or native builder pools per architecture plus manifest lists.
Supply-chain requirements increasingly ask for signed images (cosign, possibly
with keyless signing, which conflicts with air-gapped installs).

Decision
--------

Not decided yet. This ADR records the question and the options so they can be
evaluated; it changes nothing until it is accepted. Post-v1.

Alternatives Considered
-----------------------

Single platform, unsigned (v1)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Small scope.

Disadvantages:

- No multi-arch; no signing.

``buildah --platform`` with qemu on build agents
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- One pool.

Disadvantages:

- Slow; needs binfmt setup in pods.

Native builder pools per arch plus a manifest step
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Fast, native builds.

Disadvantages:

- More pools; orchestration across steps.

Signing with cosign and key-based signing via secret refs
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Works air-gapped.

Disadvantages:

- Key management; cosign license check (Apache-2.0).

Decision Criteria
-----------------

- Air-gap compatibility.
- Build pool security (ADR-0007).
- User demand.

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

- If signing is accepted, add a ``signing_key`` secret type.

Validation
----------

Nightly build against a local registry for each supported architecture; signature verification test.

References
----------

- :doc:`/architecture/design` §6.5, §22 item 9
