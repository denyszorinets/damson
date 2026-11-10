---
id: pf-r8-admin-area
title: "R8: Admin area"
lane: s
status: shipping
tracker: null
sources: []
roadmap: docs/roadmap/ui-project-first.md#R8
todo: []
base_ref: 990e68fc67c17cdb371244aa282e94ea9c5809d3
approvals:
  plan: 2026-11-10
verified: "cd web && npm run lint && npm run check && npm run test:unit: pass"
assumptions:
  - /access stays as My access for every signed-in user; the instance-admin check moves to /admin/access-check.
  - "Log purge had no UI: a small page with a confirmation calls DELETE /executions/{id}/logs."
  - "The /projects index (project admin) stays until R11; the Projects link stays in the top bar."
  - "A bearer-token operator counts as an admin for the gear and Admin area, as before."
fix: []
defects: []
audit:
  verdict: PASS
  round: 0
  must: []
  fingerprint: 7601eec8cea1ece83d25d6a36b96f6d8730241d3
  at: 2026-11-10
ship:
  prepared: 2026-11-10
  fingerprint: 18d8822b270ab36f73c7bcaa66de8959ca3b34ba
  tree: a9bec4c43ef0b3e66a578bd90e2d3f57e4d0ae41
blocker: null
---

# R8: Admin area

## Goal
Instance-wide tools live under /admin with their own left nav and a gear for instance admins; the old top-level routes redirect.

## Seams
- Admin layout guard, redirects and pages: e2e with the OIDC users and the default suite.

## Steps
1. Move the pages under /admin, layout and gear, access-check and log-purge pages, redirects → verify: svelte-check, lint
2. e2e: gear and nav for alice, not-found for bob, redirects, purge confirm → verify: both e2e suites
3. Docs and changelog → verify: make docs-check

## Done when
- alice sees the gear and every Admin page; bob has no gear and /admin/audit is a not-found view.

## Non-goals
- Deleting the /projects index (R11).

## Audit

PASS, round 1. No Must from the reviewer. Shoulds are filed as follow-ups.
