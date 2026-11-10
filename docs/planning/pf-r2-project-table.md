---
id: pf-r2-project-table
title: "R2: project page table of child projects and jobs with last-run columns"
lane: s
status: shipping
tracker: null
sources: []
roadmap: docs/roadmap/ui-project-first.md#R2
todo: []
base_ref: 6392f7beab418656581cae3b1ee999db5747ff11
approvals:
  plan: 2026-11-10
verified: "cd web && npm run lint && npm run check && npm run test:unit && cd .. && go test ./internal/api/ ./internal/store/... -count=1: pass"
assumptions:
  - "Last-run fields ride on the TreeJob items of GET /tree?parent= (the jobs-in-project read the UI uses); computed with one grouped window-function query per project (LatestRunsByJobAndStatus) and folded in Go. Upgrade to a summary table if a project has very many runs."
  - "The front page shows direct child projects and own jobs (folder), not the whole subtree; a bearer token keeps the flat JobsView list because it has no session for /tree."
  - "Duration is that of the newest finished run, from its start; none when it never started."
  - "Postgres runs of the new store test are not run locally (no Docker); CI covers them."
fix: []
defects: []
audit:
  verdict: PASS
  round: 0
  must: []
  fingerprint: 5a8e8dbb60343c984c45c973ef090cd836a87738
  at: 2026-11-10
ship:
  prepared: 2026-11-10
  fingerprint: af5e59639310678094bd8ae5a14557d788e21783
  tree: cc5caf35ec7577f88e54640e923ff70c655d74e7
blocker: null
---

# R2: project page table of child projects and jobs with last-run columns

## Goal
A project page is a folder: child projects and its own jobs in one table with status, last success, last failure and duration per job.

## Seams
- `store.FoldLatestRuns` (unit) and `JobRunStats` on both databases (storetest suite).
- `GET /tree?parent=` job fields: API test with the OIDC users, both backends.
- Project table: e2e with the OIDC users.

## Steps
1. Add the grouped query and `JobRunStats` for SQLite and Postgres → verify: storetest `JobRunStats`, `TestFoldLatestRuns`
2. Add the fields to `TreeJob`, regenerate → verify: `TestTreeJobsCarryTheirLastRuns`
3. `ProjectTable` and formatting helpers → verify: vitest `formatDuration`; svelte-check
4. e2e for the folder table, last-run cells and a hidden project → verify: e2e-oidc suite
5. Docs and changelog → verify: `make docs-check`

## Done when
- `acme` shows its child projects and jobs with last-run columns; a user with a job-only rule sees none of them.

## Non-goals
- Project-scoped nodes, saved views, sorting and filtering of the table.

## Audit

PASS, round 1. No Must from architect, spec or standards. Postgres runs of the new store and API tests were not possible locally (no Docker); CI covers them.
