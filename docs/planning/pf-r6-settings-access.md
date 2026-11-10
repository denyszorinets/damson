---
id: pf-r6-settings-access
title: "R6: project Settings and Access tabs"
lane: s
status: shipping
tracker: null
sources: []
roadmap: docs/roadmap/ui-project-first.md#R6
todo: []
base_ref: 67cdddd2c68343e78ace4fce38d5bd93e8b8cbae
approvals:
  plan: 2026-11-10
verified: "cd web && npm run lint && npm run check && npm run test:unit: pass"
assumptions:
  - "Access tab shows rules read-only; editing stays on the Projects page until R11. The effective-access check shows only for instance admins because POST /access/check is instance-admin only (a project-scoped check is a backend change, not in this roadmap)."
  - "Settings edits the description only; name and path never change. Delete shows for those who can delete (admin on the parent, or instance admin for a root); the server decides."
  - "The URL of an admin tab for a non-admin renders the not-found view, as for a hidden project."
fix: []
defects: []
audit:
  verdict: PASS
  round: 0
  must: []
  fingerprint: 47bcd6363111fb7ad1aa3e98e04d36fd8e4aff05
  at: 2026-11-10
ship:
  prepared: 2026-11-10
  fingerprint: 0be4ff83ab27791c98625e9228b98694a6585e7d
  tree: da90db047cef567bd99dc3857899f3814902d9b7
blocker: null
---

# R6: project Settings and Access tabs

## Goal
A project has Access (rules read-only, effective-access check for instance admins) and Settings (description, delete) tabs, shown only to its admins.

## Seams
- The project route and nav against the OIDC users: e2e with an instance admin, a runner, a viewer and a project admin.

## Steps
1. `updateProjectDescription`, role-gated `ProjectNav`, `ProjectSettings`, `ProjectAccess`, route gating → verify: svelte-check, lint
2. e2e for admin, runner, viewer and project-admin users, description edit, delete → verify: e2e-oidc suite
3. Docs and changelog → verify: `make docs-check`

## Done when
- An admin of a project sees Settings and Access; a viewer sees neither tab and the URLs read as not found.

## Non-goals
- Editing access rules here; a project-scoped access check (backend).

## Audit

PASS, round 1. No Must from the reviewer. Shoulds are filed as follow-ups.
