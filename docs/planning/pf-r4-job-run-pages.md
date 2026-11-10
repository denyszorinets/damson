---
id: pf-r4-job-run-pages
title: "R4: job and run pages under the project URL with tabs"
lane: s
status: shipping
tracker: null
sources: []
roadmap: docs/roadmap/ui-project-first.md#R4
todo: []
base_ref: ec400cae40b4fe20a6ba4add9702844187d461e2
approvals:
  plan: 2026-11-10
verified: "cd web && npm run lint && npm run check && npm run test:unit: pass"
assumptions:
  - "Old /jobs/<id> resolves the job project and replaces itself; old /runs/<id> follows run to job to project, and falls back to showing the run in place when the project cannot be resolved (also keeps mocked e2e working). A job-rule-only user lands on a URL with the job project (the old job page already showed it)."
  - Schedule and Webhooks tabs reuse JobTriggers with an only prop and stay instance-admin gated; R13 makes them role based.
  - Edit and New job pages keep their old URLs until R5.
  - The run page keeps its tests unchanged because the old URL still works.
fix: []
defects: []
audit:
  verdict: PASS
  round: 0
  must: []
  fingerprint: 015ec06f9d01d6c87fa1e1cfbad6981ce238f946
  at: 2026-11-10
ship:
  prepared: 2026-11-10
  fingerprint: 68166028f7c562832c4282566aa5939ae117481e
  tree: d6280658a1b717eb6b09b287228b68b943d04bbc
blocker: null
---

# R4: job and run pages under the project URL with tabs

## Goal
A job opens at /projects/<path>/-/jobs/<id> with Run, Runs, Definition, Schedule and Webhooks tabs, a run at /projects/<path>/-/runs/<id>; old URLs redirect.

## Seams
- jobRest/runRest/jobTab helpers (unit); JobView, RunView and the old routes (e2e with OIDC users and the default suite).

## Steps
1. Helpers and unit tests; JobView, JobRuns, RunView; old routes become redirects; links updated → verify: svelte-check, lint, vitest
2. e2e: table to job, tabs, wrong project corrected, old URLs move, hidden job not found → verify: both e2e suites
3. Docs and changelog → verify: make docs-check

## Done when
- From the project table a job opens at its project URL with tabs; the old URL redirects; a viewer sees the run form disabled with the missing role.

## Non-goals
- Tabbed editor (R5); role-based Schedule and Webhooks tabs (R13).

## Audit

PASS, round 1. No Must from the reviewer. Shoulds are filed as follow-ups.
