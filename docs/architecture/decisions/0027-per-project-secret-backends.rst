ADR-0027: Per-project secret backends
================================================================================

:Status: Proposed
:Date: 2026-10-27

Context
-------

v1 configures one secret backend globally (``db`` or ``openbao``). Some
organisations want different teams' secrets in different vaults or mounts. The
``SecretStore`` interface already takes a ref that includes the project path,
so routing by project is possible.

Decision
--------

Not decided yet. This ADR records the question and the options so they can be
evaluated; it changes nothing until it is accepted. Post-v1.

Alternatives Considered
-----------------------

Global backend only
~~~~~~~~~~~~~~~~~~~

Advantages:

- Simple configuration and audit.

Disadvantages:

- One vault for everyone.

Backend per project subtree, inherited
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Team autonomy; matches the project tree model.

Disadvantages:

- Configuration UI and validation; controller needs credentials for every backend.

Decision Criteria
-----------------

- Security isolation between teams.
- Operational complexity.
- Backward compatibility of ``secret://`` refs.

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

- Must not change the ``secret://`` ref format.

Validation
----------

Masking and delivery tests across mixed backends; security review.

References
----------

- :doc:`/architecture/design` §9.2, §22 item 7
- :doc:`0015-secret-references-and-envelope-encryption`
