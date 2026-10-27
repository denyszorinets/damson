ADR-0009: A pure, deterministic orchestration engine
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Job specs support sequential steps, ``when`` conditions, groups with
try/catch/finally, retries with backoff, timeouts, cancellation and ``for_each``
lanes. These semantics must behave identically on every controller, survive
controller restarts mid-run, and be exhaustively testable. The UI also needs the
job structure without re-deriving control flow.

Decision
--------

- ``internal/engine`` exposes a pure function
  ``Decide(spec, state, now) -> []Action`` with actions such as
  ``CreateStepInstance``, ``ScheduleExecution``, ``SkipStep``,
  ``CompleteGroup``, ``CompleteRun`` and ``CancelExecution``.
- The engine has no I/O, does not call ``time.Now()`` and uses no randomness
  (both injected). All control-flow semantics (ARCHITECTURE §5.3) live there and
  nowhere else.
- Controllers run ``run.advance``: load state, call ``Decide``, apply actions
  in one transaction with optimistic concurrency on ``runs.version``.
- ``engine.BuildGraph(spec)`` is the only source of the job and run graph
  (:doc:`0020-server-built-job-graph-with-in-house-layout`).

Alternatives Considered
-----------------------

Temporal as the engine
~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Heavy dependency with its own cluster; harder self-hosting and air-gapped
  installs.

Actor framework
~~~~~~~~~~~~~~~

Disadvantages:

- Goroutine per execution plus a pure engine plus database CAS already covers
  supervision; durability comes from Postgres, not actors.

Imperative orchestration inside handlers
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Semantics scattered across I/O code; hard to test and to resume after
  crashes.

Decision Criteria
-----------------

Correctness, testability, resumability, dependency weight.

Rationale
---------

A pure function over persisted state makes every controller interchangeable and
lets every semantic rule be pinned by table and golden tests.

Consequences
------------

Positive
~~~~~~~~

- Rules are tested without databases or networks.
- Any controller can resume any run.

Negative
~~~~~~~~

- Run state must be loaded per advancement; large fan-outs need efficient
  state loading.

Risks
~~~~~

- Semantics leaking into the store or API. Mitigation: hard rule 1, review.

Follow-up
~~~~~~~~~

- M2: linear specs; M3: full §5.3.

Validation
----------

Table and golden tests in ``testdata/engine/`` for every rule in §5.3,
including retries, lanes, catch/finally and cancel.

References
----------

- :doc:`/architecture/design` §5.3, §8.2, §23
