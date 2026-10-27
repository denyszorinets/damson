ADR-0018: Huma HTTP API with a generated OpenAPI client
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

The API serves the SPA, the CLI, CI systems, inventory syncs and webhooks. Its
contract must stay in sync with the frontend and external clients without
hand-maintained specs or fetch code.

Decision
--------

- HTTP on stdlib ``net/http`` with **Huma**; OpenAPI 3.1 is generated from Go
  types and served at ``/api/v1/openapi.json``.
- All routes are under ``/api/v1``. Errors are RFC 9457 problem+json, mapped in
  one place. Pagination is cursor-based. Live data uses SSE.
- The TypeScript client is generated with ``openapi-typescript`` and
  ``openapi-fetch``; hand-written fetch code is not allowed.

Alternatives Considered
-----------------------

Hand-written OpenAPI plus a router (chi, echo)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Spec and code drift.

gRPC with grpc-gateway
~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Protobuf toolchain; browser clients need a gateway anyway.

GraphQL
~~~~~~~

Disadvantages:

- Extra complexity for a CRUD-plus-streams API.

Decision Criteria
-----------------

Contract accuracy, code volume, client generation, stdlib compatibility.

Rationale
---------

Generating the spec from types and the client from the spec makes the contract
correct by construction.

Consequences
------------

Positive
~~~~~~~~

- One source of truth for the API; typed frontend.

Negative
~~~~~~~~

- Dependence on Huma's conventions.

Risks
~~~~~

- Huma maintenance. Mitigation: it sits on stdlib ``net/http``, so handlers are
  portable.

Follow-up
~~~~~~~~~

- M0: ``/healthz``, OpenAPI generation, ``make gen`` for the TS client.

Validation
----------

``make gen`` produces no diff in CI.

References
----------

- :doc:`/architecture/design` §2, §14
