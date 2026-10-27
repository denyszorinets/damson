ADR-0008: Fencing tokens and execution leases
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Executions run on agents while controllers come and go. Agents reconnect to a
different controller and replay results. A slow or partitioned agent may report
an outcome after the execution has been declared lost and retried elsewhere.
Without protection, a stale result could overwrite a newer attempt or complete
a step twice.

Decision
--------

- Every Execution has a unique ``attempt_id`` (UUIDv7) acting as a **fencing
  token**. Logs, completions, lease renewals and host key reports are applied
  only if their ``attempt_id`` matches the execution's current attempt and the
  execution is not terminal. Stale results are acknowledged and discarded.
- Each running Execution has ``lease_expires_at = now + 60s``. Heartbeats
  (every 10 s) renew leases for listed attempts in batched updates. The reaper
  duty marks expired leases ``lost``.
- ``step.complete`` is retried by the agent until acknowledged, across
  reconnects; the controller acks only after commit.
- ``lost`` is retried only for idempotent plugins or with
  ``retry.on_lost: true``.

Alternatives Considered
-----------------------

Trust the latest report
~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Double completion and lost updates under partitions.

Exactly-once delivery via a broker
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Still needs fencing at the state layer; adds infrastructure
  (:doc:`0005-no-message-broker`).

Decision Criteria
-----------------

Correctness under partitions and restarts, simplicity, testability.

Rationale
---------

Fencing plus CAS is the standard, minimal way to make at-least-once delivery
safe. Leases bound how long a vanished agent can hold work.

Consequences
------------

Positive
~~~~~~~~

- No execution completes twice; agents can replay results freely.

Negative
~~~~~~~~

- A partitioned agent's late work is discarded even if it succeeded.
- Non-idempotent steps whose agent vanished are not retried automatically.

Risks
~~~~~

- Any code path that skips the ``attempt_id`` check breaks safety (hard rule 3).

Follow-up
~~~~~~~~~

- M2: leases and fencing on dispatch; M3: reaper.

Validation
----------

Property tests over fencing and lease logic; chaos test assertion "no
execution completes twice".

References
----------

- :doc:`/architecture/design` §7.3, §8.3
- :doc:`0004-database-as-source-of-truth-without-consensus`
