---
id: pf-r14-secrets-authz
title: "R14: project-scoped authorization for secrets (ADR-0077)"
lane: s
status: shipping
tracker: null
sources:
  - docs/architecture/decisions/0077-project-scoped-secrets.rst
roadmap: docs/roadmap/ui-project-first.md#R14
todo: []
base_ref: 080ef0939e0de6629f515340999c6c5d06fa6585
approvals:
  plan: 2026-11-10
verified: "go vet ./internal/... && go test ./internal/api/ ./internal/secrets/... ./internal/store/... ./internal/agent/... ./internal/controller/... -count=1 && cd web && npm run lint && npm run check && npm run test:unit: pass"
assumptions:
  - "D1 defaults as designed by architect: list editor, put and delete admin on the scope project; ancestor names visible to an editor through project=P (maintainer-recommended yes)."
  - A site-wide secret scope is out of scope; _inventory and orphan scopes are instance admin only.
  - The authz test world now carries a database secret backend so the secrets routes can be exercised there.
fix: []
defects: []
audit:
  verdict: PASS
  round: 0
  must: []
  fingerprint: 164d6cf57917cc06aa7aa667ef8e7e92f7f94928
  at: 2026-11-10
ship:
  prepared: 2026-11-10
  fingerprint: 8413e8dbc0ea2918c55059ff43b0b4f4c20ae8f2
  tree: e02eed7fbb8dccb599a4fcb3de8b4e8912f94d5d
blocker: null
---

# R14: project-scoped authorization for secrets (ADR-0077)

## Goal
Secrets follow the roles of their scope's project as ADR-0077 states; values are never returned, audited or logged.

## Seams
- The three secrets routes through the authorization middleware and the handlers: authzWorld tests with the seven users and service tokens, both backends.
- A canary value test over responses, audit rows and logs, denied paths included.

## Steps
1. Route policies, `requireScope`, list by scope or project, values cleared on every path → verify: `TestSecretRoutesFollowTheRolesOfTheirScope`, encoded-scope and token tests
2. Canary test → verify: `TestSecretValueNeverAppearsInResponsesAuditOrLogs`
3. Docs, changelog, generated OpenAPI → verify: `make gen` clean, `make docs-check`

## Done when
- Admin of `acme/payments` creates and reads the name of a secret there and gets 404 on another project's; an editor sees names only; a viewer sees nothing; no response or audit row holds a value; the dispatch masking test passes.

## Non-goals
- The Secrets tab (R7); a site-wide secret scope.

## Audit

PASS, round 1. No Must from architect or from spec and standards. The Postgres runs of the API and store tests could not run locally (no Docker); CI covers them. Shoulds are fixed in a follow-up commit (OpenAPI description of `project`, error lists, paging and tampered-cursor tests, ADR audit wording, ARCHITECTURE line) or filed.
