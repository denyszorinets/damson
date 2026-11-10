---
id: pf-r9-home-search
title: "R9: Home project search and shared jobs"
lane: s
status: shipping
tracker: null
sources: []
roadmap: docs/roadmap/ui-project-first.md#R9
todo: []
base_ref: 271b82e52b0661b50c04cea4224239492589d73c
approvals:
  plan: 2026-11-10
verified: "cd web && npm run lint && npm run check && npm run test:unit: pass"
assumptions:
  - "Shared with me already shipped in R1 (jobs through a job rule, no project shown); R9 adds the search and pins it in e2e."
  - "The search is client-side over GET /projects (every visible project, nested too), loaded on the first keystroke; a plain text matches anywhere in the path case-insensitively, /regex/ is case-insensitive unless flags are given."
  - The search box shows only for users with projects; a job-rule-only user has none.
fix: []
defects: []
audit:
  verdict: PASS
  round: 0
  must: []
  fingerprint: 4f668ef0bbca534d4e25a8435be3450ffeed4557
  at: 2026-11-10
ship:
  prepared: 2026-11-10
  fingerprint: a87cb3a4484498dd2d62da6d3409634eb1007e97
  tree: 93130d66e16e2e5439b3933e394c8bea7a5ec2b0
blocker: null
---

# R9: Home project search and shared jobs

## Goal
Home lists jobs reached only through a job rule under "Shared with me" and searches the visible projects by name or /regex/.

## Seams
- `projectMatcher` (unit); Home search against the OIDC users (e2e).

## Steps
1. `projectMatcher`, Home search box over `GET /projects` → verify: vitest `projectMatcher`; svelte-check
2. e2e: search by name, nested match, regex, invalid regex, no match, hidden projects never match, job-rule-only user → verify: e2e-oidc suite
3. Docs and changelog → verify: make docs-check

## Done when
- frank sees one shared job and no project; searching `pay` lists only visible projects.

## Non-goals
- Server-side search; saved views.

## Audit

Round 1 FAIL: user-typed g/y regex flags made matching depend on earlier paths, and a failed project fetch left a stale error and an endless "Searching…". Fixed (flags stripped with tests, error reset per attempt, in-flight guard). Round 2 PASS, no Must.
