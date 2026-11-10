---
id: pf-r7-project-secrets
title: "R7: project Secrets tab"
lane: s
status: shipping
tracker: null
sources: []
roadmap: docs/roadmap/ui-project-first.md#R7
todo: []
base_ref: 1f7a3550c847c2e048383a4321ed6c73aac5c2b3
approvals:
  plan: 2026-11-10
verified: "cd web && npm run lint && npm run check && npm run test:unit: pass"
assumptions:
  - "The tab shows for editor and up (names), writes for admin, per ADR-0077; ancestors rows are read-only on the child (writing an ancestor secret is done on the ancestor)."
  - Writes use the session cookie like the admin Secrets page; the admin /secrets page stays for all-scope management until R8.
  - Only the own scope offers New version and Delete.
fix: []
defects: []
audit:
  verdict: PASS
  round: 0
  must: []
  fingerprint: ecbfd6a498488c9d6cedeea5ca446a5ad581bc83
  at: 2026-11-10
ship:
  prepared: 2026-11-10
  fingerprint: 2d37b07ab15990ed84f6fb831b845d9947c9fb23
  tree: 8fb0b6c0668c47203b7c8b125d499287fe73dfa9
blocker: null
---

# R7: project Secrets tab

## Goal
The project Secrets tab lists own and inherited secrets with their origin, with create, new version and delete by role, and no value shown.

## Seams
- ProjectSecrets and the route against the OIDC users (e2e): admin, editor, runner, viewer, project admin.

## Steps
1. `fetchProjectSecrets`, `ProjectSecrets`, nav and route gating by role → verify: svelte-check, lint
2. e2e: inherited label, create, no value in the page, editor read-only, runner and viewer without the tab, delete → verify: e2e-oidc suite
3. Docs and changelog → verify: make docs-check

## Done when
- An admin creates a secret on a child project and sees it beside the inherited one, labelled; no value in the DOM; a viewer has no Secrets tab.

## Non-goals
- A site-wide scope; editing an ancestor secret from the child.

## Audit

PASS, round 1. No Must from the reviewer. Shoulds are fixed in a follow-up commit (error and list shown together, paging cap notice, type-fetch error kept, a masking test for a failed save) or filed.
