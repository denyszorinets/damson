ADR-0031: Manual approval steps and notifications
================================================================================

:Status: Proposed
:Date: 2026-10-27

Context
-------

Production deploys often need a human gate ("approve to continue") and
notifications (email, Slack, Teams). §22 proposes both as plugins. An approval
gate is unusual for a plugin: it waits for a human, possibly for hours, so it
should not hold an agent slot. That suggests engine-level support (a waiting
state resolved through the API) rather than a plugin that blocks.

Decision
--------

Not decided yet. This ADR records the question and the options so they can be
evaluated; it changes nothing until it is accepted. Post-v1.

Alternatives Considered
-----------------------

Approval as a blocking plugin
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- No engine change.

Disadvantages:

- Holds an agent slot and a lease for hours; survives restarts poorly.

Approval as an engine step type (``type: approval``)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- No agent involved; durable waiting state in the DB; RBAC on who may approve.

Disadvantages:

- Engine semantics and spec format change.

Notifications as ordinary plugins
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Fits the plugin model; schema-driven forms.

Disadvantages:

- Outbound network from agents; webhook secrets as refs.

Decision Criteria
-----------------

- Resource use while waiting.
- Engine purity and spec format stability.
- Air-gap (notification endpoints are often internal).

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

- Approval semantics need an engine design in plan mode before acceptance.

Validation
----------

Engine golden tests for the waiting state; plugin contract suite for notification plugins.

References
----------

- :doc:`/architecture/design` §22 item 6
- :doc:`0009-pure-deterministic-engine`
