---
id: pf-r12-trigger-authz
title: "R12: project-scoped authorization for schedules and webhooks (ADR-0076)"
lane: s
status: shipping
tracker: null
sources:
  - docs/architecture/decisions/0076-project-scoped-schedules-and-webhooks.rst
roadmap: docs/roadmap/ui-project-first.md#R12
todo: []
base_ref: 96eade8c65ffe456109624fdfd8b5ae97ec69d20
approvals:
  plan: 2026-11-10
verified: "go vet ./internal/... && go test ./internal/api/ ./internal/store/... ./internal/secrets/... -count=1 && cd web && npm run lint && npm run check && npm run test:unit: pass"
assumptions:
  - "D1 defaults, reviewed by architect: schedules viewer/editor, webhooks viewer/admin, job rules count, tokens by role then scope. Maintainer defaults taken: runner cannot pause; viewers see webhook metadata including options_mapping."
  - "A scoped service token must pass job_id or project when listing (a job-less list resolves to every project and a scoped token is out of scope: 404). Revisit if tokens need an implicit project."
  - Instance admins keep the old behaviour of an empty list for a missing job_id.
  - Secret-ref scoping for scheduled runs is unchanged and covered by internal/secrets Authorize tests; no new test for the fire path.
fix: []
defects: []
audit:
  verdict: PASS
  round: 0
  must: []
  fingerprint: 68f575e649a06504fb0ef49f7d3dbd8f7a7bc945
  at: 2026-11-10
ship:
  prepared: 2026-11-10
  fingerprint: fc791a1b79a4eeeee8140cca01dae2d2edcb48fe
  tree: e96cf85ffb5d96f07caf746aa86d00a7f8e7f884
blocker: null
---

# R12: project-scoped authorization for schedules and webhooks (ADR-0076)

## Goal
Schedules and webhooks follow the roles of their job's project as ADR-0076 states: viewer reads, editor writes schedules, admin creates and revokes webhooks; 404 without a role; list by `project` subtree; audit events carry the project.

## Seams
- The nine routes through the authorization middleware and handlers: authzWorld tests with seven users and service tokens on both backends.
- `ListSchedulesByProject` / `ListWebhooksByProject` and the audit project path: storetest on both databases.

## Steps
1. Store: by-project lists and the audit project path → verify: storetest `TriggersByProject`
2. API: route policies, targets, handler checks, list filters → verify: `TestScheduleAndWebhookRoutesFollowProjectRoles`, job-rule, list, token and audit tests
3. Regenerate OpenAPI and TS client; docs, changelog → verify: `make gen` clean, `make docs-check`

## Done when
- Editor of `acme/payments` creates a schedule on its job and gets 404 on another project's; a viewer gets 403 on write and can read; instance admin unchanged.

## Non-goals
- UI tabs (R13), project secrets (R14).

## Audit

PASS, round 1. No Must from architect or from spec and standards. The Postgres runs of the new store and API tests could not run locally (no Docker); CI covers them.
