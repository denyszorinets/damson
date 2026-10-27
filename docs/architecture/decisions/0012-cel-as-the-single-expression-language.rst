ADR-0012: CEL as the single expression language
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Damson needs expressions in many places: step conditions (``when``), retry
filters (``retry_on``), node selectors, ``${{ }}`` interpolation, project node
scopes, authorization rules over OIDC claims, and webhook option mapping.
Expressions are written by users and evaluated on controllers, so they must be
safe, bounded and side-effect free.

Decision
--------

Use **CEL** (``github.com/google/cel-go``) everywhere an expression is needed.

- ``internal/expr`` defines one CEL environment per use (spec, selector, authz,
  webhook) with the variables listed in ARCHITECTURE §5.4.
- Programs run with a cost limit and a result size limit. No I/O functions are
  exposed.
- Secrets are never values in the CEL context; they appear only as
  ``secret://`` strings.
- Authorization rules are compiled once per change and cached per session.

Alternatives Considered
-----------------------

Go templates / Jinja-like templating
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Stringly typed; unsuitable for boolean policies; Turing-complete risks.

Expr (``expr-lang/expr``)
~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Fast, friendly syntax.

Disadvantages:

- No standard cost model; less adoption for policy (Kubernetes uses CEL).

Rego / OPA for authorization
~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- A second language for one use case; heavier runtime.

Decision Criteria
-----------------

Safety (bounded cost, no side effects), typing, one language for all uses,
ecosystem familiarity.

Rationale
---------

One expression language means one set of docs, one evaluator and one security
review. CEL was designed for exactly this: safe, typed, bounded evaluation.

Consequences
------------

Positive
~~~~~~~~

- Users learn one syntax; Kubernetes users already know it.

Negative
~~~~~~~~

- CEL syntax is unfamiliar to shell-script authors.

Risks
~~~~~

- Expensive expressions on large inventories. Mitigation: cost limits.

Follow-up
~~~~~~~~~

- M2: interpolation; M3: ``when`` and ``retry_on``; M4: selectors; M8: authz.

Validation
----------

Unit tests for each environment, including cost-limit rejection.

References
----------

- :doc:`/architecture/design` §5.3, §5.4, §5.5, §12.4
