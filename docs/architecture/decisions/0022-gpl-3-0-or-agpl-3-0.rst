ADR-0022: Project license: GPL-3.0-or-later or AGPL-3.0-or-later
================================================================================

:Status: Proposed
:Date: 2026-10-27

Context
-------

Damson is currently licensed GPL-3.0-or-later. The GPL's obligations trigger
on distribution; offering a modified Damson as a hosted service does not
require sharing changes. AGPL-3.0 closes that gap (network use counts) but may
deter some corporate adopters whose policies forbid AGPL software. The license
should be settled before external contributions arrive, because changing it
later may need every contributor's consent (unless a CLA or DCO-plus-"or later"
arrangement allows it).

Decision
--------

Not decided yet. This ADR records the question and the options so they can be
evaluated; it changes nothing until it is accepted. Target: before M0 closes.

Alternatives Considered
-----------------------

Keep GPL-3.0-or-later
~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Current state; broadly accepted by enterprises.
- Compatible with the dependency policy as is.

Disadvantages:

- Hosted forks need not share modifications.

Switch to AGPL-3.0-or-later
~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Hosted modified versions must offer their source.

Disadvantages:

- Some organisations ban AGPL software outright.
- Requires reviewing dependency compatibility (allowed licenses are AGPL-compatible too).

Decision Criteria
-----------------

- Adoption in target organisations (internal ops teams, air-gapped sites).
- Protection against closed hosted forks.
- Contributor and dependency compatibility.

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

- Decide before accepting the first external contribution.
- Update LICENSE, README, ARCHITECTURE.md header and ADR-0021 if the license changes.

Validation
----------

Legal review of the chosen license against the dependency list in ADR-0021.

References
----------

- :doc:`/architecture/design` §22 item 1
- :doc:`0021-dependency-license-policy`
