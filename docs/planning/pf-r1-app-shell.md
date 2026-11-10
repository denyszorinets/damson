---
id: pf-r1-app-shell
title: "R1: app shell, project switcher, breadcrumb, Home as projects table"
lane: s
status: shipping
tracker: null
sources: []
roadmap: docs/roadmap/ui-project-first.md#R1
todo: []
base_ref: 96dc104f667aa1c8845c01fb930f91ce680209d2
approvals:
  plan: 2026-11-10
verified: "cd web && npm run lint && npm run check && npm run test:unit: pass"
assumptions:
  - "Home table lists root projects from /tree (reversible); the switcher lists every visible project from /projects. Child count column shows has_children (yes/no) because /tree gives no count; upgrade if R2 adds a count."
  - "Recent runs and failures leave Home (they move to project Activity in R3); Shared with me stays on Home as is until R9."
  - "Old /jobs/<id>, /runs/<id> and /jobs/new stay until R4; only the /jobs list redirects to Home now."
  - "Route scheme: one catch-all projects/[...path] parses <path>/-/<tab>; unknown tab renders the not-found view; only no-tab is implemented in R1. Stop-if-wrong: SvelteKit rest param constraint."
fix: []
defects: []
audit:
  verdict: PASS
  round: 0
  must: []
  fingerprint: 19e4b665639f1355eb967f8981b17a284ae6fad3
  at: 2026-11-10
ship:
  prepared: 2026-11-10
  fingerprint: b5fa620931d91039f05684b56a5da6f784d853b7
  tree: 246ecc9e0eddada778bd590c6a8c604ab32495b7
blocker: null
---

# R1: app shell, project switcher, breadcrumb, Home as projects table

## Goal
Home lists projects (name, child indicator, jobs, failed in 24 h) and no job except "Shared with me". The top bar holds a project switcher and a path breadcrumb; the sidebar tree and the `/jobs` list are gone from navigation, and `/jobs` redirects to Home. Project URLs use `/projects/<path>[/-/<tab>]`.

## Seams
- `parseProjectRoute(rest)` in `lib/projectroute.ts`: splits `a/b/-/tab` into path and tab; unit tests catch the parsing and rejects.
- `breadcrumb(path)`: crumbs with links; unit tests.
- Layout switcher and Home table: e2e against `damson dev` and the OIDC users.

## Steps
1. Add `lib/projectroute.ts` (parse, build href, breadcrumb) → verify: vitest cases for no tab, a tab, nested path, leading/trailing slashes, `-` inside a name
2. Rework `+layout.svelte`: top bar with switcher (all visible projects, filterable), breadcrumb from the route, Main links trimmed, ProjectTree and TreeNode no longer rendered → verify: svelte-check; e2e switcher lists the caller's visible set
3. Rework Home: projects table from `fetchLevel('')`, Shared with me kept, recent runs and failures removed → verify: e2e bob's home lists only his projects and no job
4. `/jobs` list route redirects to `/`; `projects/[...path]` page uses `parseProjectRoute` and shows the existing job list for the path, not-found for an unknown tab → verify: e2e `/jobs` lands on Home; `/projects/x/-/nope` is not-found
5. Update e2e and e2e-oidc tests that used the sidebar, home runs or `/jobs`; update the UI reference doc → verify: `npm run lint`, `npm run check`, `npm run test:unit`, e2e both configs

## Done when
- bob's home lists only his projects and no job; the switcher lists the same set (and nested ones); `/jobs` redirects to Home.

## Non-goals
- Project page table with last-run columns (R2); tabs and sidebar (R3); Admin split (R8); deleting ProjectTree files from the repo is done here only if unused, else R11.

## Audit

PASS, round 1. Mechanical checks green (web lint, svelte-check, unit; both e2e suites except failures that also fail on the untouched tree: agents `os=linux`, rundialog). No Must from Spec or Standards; guard-it scan found only a pre-existing untracked `deploy/dev/` Keycloak realm file, outside this change.

Should, carried forward: unused `crumbs` in `lib/jobs.ts` and `fetchRecentRuns`/`jobsById` in `lib/runs.ts` (R11 contract); switcher not filterable (R10); breadcrumb links ancestors the caller may not see (R2/R3 shell work); hand-built project hrefs without `projectRest` on job pages (R4 moves them).
