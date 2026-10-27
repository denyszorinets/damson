ADR-0017: OIDC authentication and CEL authorization on a project tree
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Organisations already run an identity provider. Access must be delegated per
team and per subtree of projects (``infra``, ``infra/billing``), driven by IdP
group claims, without Damson managing users or passwords. Machines (CI, CMDB
sync) need API access. Any controller must serve any request.

Decision
--------

- **UI**: OIDC authorization code flow with PKCE (``coreos/go-oidc/v3``,
  ``x/oauth2``). Server-side sessions in the database; HttpOnly, Secure,
  SameSite=Lax cookie; claims snapshotted into the session.
- **API**: bearer service-account tokens (random, hashed, scoped by project
  subtree and role, optional expiry) or OIDC JWT access tokens from the
  configured issuer.
- **Projects** form a tree with a materialized ``path``; access rules, node
  scope, retention and secrets inherit from ancestors.
- **Roles**: ``viewer`` < ``runner`` < ``editor`` < ``admin``.
- **Rules** are CEL over ``claims`` and ``user``, attached to projects
  (inherited) and optionally jobs (additive). Effective role is the maximum of
  all matching rules.
- **Bootstrap**: a config CEL rule for admins plus a one-time local recovery
  token printed at first start.
- Every mutation and trigger writes an audit event.

Alternatives Considered
-----------------------

Local users and passwords
~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Damson becomes an identity system; weaker security posture.

Static role bindings (user/group → role tables)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Cannot express claim-based conditions; another model beside CEL.

Stateless JWT sessions in the browser
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Hard revocation; token size limits; refresh complexity.

Decision Criteria
-----------------

Security, delegation, integration with existing IdPs, HA (any controller).

Rationale
---------

OIDC delegates identity to the IdP; CEL rules over claims express group- and
attribute-based policies with the language already used elsewhere
(:doc:`0012-cel-as-the-single-expression-language`).

Consequences
------------

Positive
~~~~~~~~

- No passwords stored; groups managed in the IdP.

Negative
~~~~~~~~

- An OIDC provider is required for UI login in production.

Risks
~~~~~

- Overly broad rules. Mitigation: audit log and an effective-role view.

Follow-up
~~~~~~~~~

- M7: service accounts and tokens. M8: OIDC, sessions, CEL rules, audit.

Validation
----------

Playwright login smoke test with a mock OIDC provider; authz table tests;
security review.

References
----------

- :doc:`/architecture/design` §12
- :doc:`/architecture/security`
