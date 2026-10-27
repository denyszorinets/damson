ADR-0005: No message broker
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Controllers need to queue work, wake each other (cancel hints, dispatch
nudges), deliver logs from agents, and serve live log tails from any controller.
A broker such as NATS/JetStream, Redis or Kafka is a common answer.
``CLAUDE.md`` states that simplicity is a feature and that required
infrastructure is Postgres only.

Decision
--------

Damson uses **no message broker**. Postgres plus the agent WebSockets cover
queueing, coordination and log delivery:

- Work queue: the ``executions`` table claimed with ``SKIP LOCKED``; River jobs
  for run advancement (:doc:`0010-river-for-background-work`).
- Wake-ups: ``LISTEN/NOTIFY`` hints with polling fallbacks (1 s dispatch, 2 s
  cancel).
- Live tail across controllers: an authenticated internal HTTP proxy to the
  owning controller.

Introducing any broker, cache or extra service requires a new ADR.

Alternatives Considered
-----------------------

NATS / JetStream as agent transport and queue
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Would replace about 300 lines of RPC framing and the log-tail proxy.

Disadvantages:

- A second stateful component to deploy, secure and upgrade; JetStream HA is
  itself a Raft cluster.
- Postgres is still required for state, so it adds infrastructure without
  removing any.

Redis
~~~~~

Disadvantages:

- Same added-infrastructure cost; durability semantics weaker than Postgres.

Decision Criteria
-----------------

Required infrastructure, operational complexity, failure modes, code volume.

Rationale
---------

The extra code a broker would save is small and in-house; the operational cost
of a broker is permanent for every installation.

Consequences
------------

Positive
~~~~~~~~

- Installations run Damson plus Postgres, nothing else.

Negative
~~~~~~~~

- Damson owns its RPC framing and the cross-controller tail proxy.

Risks
~~~~~

- Log fan-out across controllers could become a bottleneck. Revisit only with a
  measurement showing it.

Follow-up
~~~~~~~~~

- None.

Validation
----------

Load test at the scale target (1,000 agents, ~500 concurrent executions) before
v1 release (M9).

References
----------

- :doc:`/architecture/design` §3, §8.3, §8.5, §23
