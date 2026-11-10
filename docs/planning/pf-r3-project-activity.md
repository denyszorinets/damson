---
id: pf-r3-project-activity
title: "R3: project sidebar, Overview and Activity tabs, runs project filter"
lane: s
status: shipping
tracker: null
sources: []
roadmap: docs/roadmap/ui-project-first.md#R3
todo: []
base_ref: 79a66aa12ff7798690a20dcbf23c56a4fb01abd3
approvals:
  plan: 2026-11-10
verified: "cd web && npm run lint && npm run check && npm run test:unit && cd .. && go test ./internal/api/ -count=1: pass"
assumptions:
  - "Front page of a project stays the jobs table (R2 upgrades it); Overview is -/overview and Activity -/activity. Reversible: tab names."
  - "GET /runs?project= reuses visibleRuns (bounded scan of 20 pages of 200) with a scope predicate; a sparse project in a busy instance may need More clicks. Upgrade to a store-level project filter if it shows in practice."
  - "Success rate = succeeded/(succeeded+failed) over the newest 50 runs of the project subtree; canceled and unfinished runs excluded."
  - "Sidebar has Overview, Jobs, Activity only; Secrets, Access, Settings tabs arrive with R6/R7/R13."
fix: []
defects: []
audit:
  verdict: PASS
  round: 0
  must: []
  fingerprint: 70512283fccc3d5e8ce1269031d8d440abbd86a2
  at: 2026-11-10
ship:
  prepared: 2026-11-10
  fingerprint: a19b9111b2673e74d7952b54fb56445055d10d82
  tree: 8e3dcb673fd6c75a03d9a28ee9028ab4fd019237
blocker: null
---

# R3: project sidebar, Overview and Activity tabs, runs project filter

## Goal
A project has its own sidebar (Overview, Jobs, Activity). Overview shows the success rate and recent runs; Activity lists runs filtered by status and job. `GET /runs` gets a `project` filter over the visible set; a project without a role answers 404.

## Seams
- `GET /runs?project=` in `internal/api`: API tests on SQLite and Postgres through `forEachBackend`.
- `successRate`: unit tests.
- Project tabs: e2e with the OIDC users.

## Steps
1. Add the `project` filter to list-runs, reusing `visibleRuns` and `scopeOf` → verify: `TestListRunsFilteredByProject` for alice, bob, dave, erin, frank
2. Regenerate OpenAPI and the TS client → verify: `make gen` leaves only the intended diff
3. Sidebar, Overview, Activity components and `successRate` → verify: vitest `successRate`; svelte-check
4. e2e: Activity of `tree-acme/pay` lists only its runs, a runner of another project gets 404 → verify: e2e-oidc suite
5. Docs and changelog → verify: `make docs-check`

## Done when
- Activity of `acme/payments` lists only runs of its jobs; a runner on another project cannot read them (404 as missing).

## Non-goals
- Secrets, Access, Settings tabs; last-run columns on the jobs table (R2).

## Audit

Round 1 FAIL (architect): with login off the project gate passed but the scan recomputed roles with no principal and returned nothing; `job` plus `project` silently dropped the project. Fixed: the handler passes the gate's own role map to `visibleRuns`, a job outside the project returns an empty page, tests added (login off, mismatch). Round 2 PASS: no Must from architect, spec or standards.
