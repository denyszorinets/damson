ADR-0030: Helm diff preview before upgrade
================================================================================

:Status: Proposed
:Date: 2026-10-27

Context
-------

Operators want to see what a Helm upgrade will change before it runs. The
``helm-diff`` plugin provides this but must be installed in the build-agent
image. A preview step pairs naturally with manual approval gates
(:doc:`0031-manual-approval-steps-and-notifications`).

Decision
--------

Not decided yet. This ADR records the question and the options so they can be
evaluated; it changes nothing until it is accepted. Post-v1.

Alternatives Considered
-----------------------

No preview
~~~~~~~~~~

Advantages:

- Smaller image.

Disadvantages:

- Blind upgrades.

``action: diff`` in the helm plugin using helm-diff
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Fits the existing plugin; output as logs and outputs.

Disadvantages:

- Extra binary in ``damson-agent-build``; license check (Apache-2.0).

Decision Criteria
-----------------

- Value with or without approval gates.
- Image size and supply chain.

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

- Decide together with approval gates.

Validation
----------

Nightly test against the ``kind`` cluster.

References
----------

- :doc:`/architecture/design` §6.6, §22 item 10
