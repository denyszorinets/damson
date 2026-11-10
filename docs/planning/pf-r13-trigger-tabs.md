---
id: pf-r13-trigger-tabs
title: "R13: project Schedules and Webhooks tabs, role-based job tabs"
lane: s
status: shipping
tracker: null
sources: []
roadmap: docs/roadmap/ui-project-first.md#R13
todo: []
base_ref: f6c18faca6734ad65e69a4284982f688f05cc1dd
approvals:
  plan: 2026-11-10
verified: "cd web && npm run lint && npm run check && npm run test:unit: pass"
assumptions:
  - "Project tabs list and pause or delete schedules and revoke webhooks; adding either stays on the job tab (a job must be chosen)."
  - "Controls follow ADR-0076: schedules editor, webhooks project admin, reading for any role; bearer token operators keep the admin-token flow with full controls."
  - "The job role shown to JobTriggers is job.my_role (job rules count up to editor)."
fix: []
defects: []
audit:
  verdict: PASS
  round: 0
  must: []
  fingerprint: af0510932fd2bd5bfdce0723d6752fc6edbca2ad
  at: 2026-11-10
ship:
  prepared: 2026-11-10
  fingerprint: d54fb72c2a4a9174465dd7ad60eb454fa6b02211
  tree: ea93c58e44b1a274a75dcfbdc6610b74de15e938
blocker: null
---

# R13: project Schedules and Webhooks tabs, role-based job tabs

## Goal
Projects list their jobs' schedules and webhooks in tabs, and the job tabs show controls by role.

## Seams
- ProjectTriggers, role props of JobTriggers: e2e with the OIDC users.

## Steps
1. `fetchProjectSchedules`, `fetchProjectWebhooks`, `ProjectTriggers`, role-aware `JobTriggers`, nav and route → verify: svelte-check, lint
2. e2e: admin pauses and resumes, viewer and runner read-only, editor schedules but not webhooks, no role refused, webhook revoke → verify: e2e-oidc suite
3. Docs and changelog → verify: make docs-check

## Done when
- An editor lists and pauses a schedule in the project, a viewer sees them read-only, a user without a role sees a not-found style error.

## Non-goals
- Creating a schedule or webhook from the project tab.

## Audit

Round 1 FAIL: Schedules and Webhooks project tabs shared one component instance, so switching tabs showed a stale or empty list for up to 15 s. Fixed by keying the component per tab; trigger tabs are hidden without a login; the role hints wait for the role; a 200-row cap errors; the e2e switches tabs without a reload. Round 2 PASS, no Must.
