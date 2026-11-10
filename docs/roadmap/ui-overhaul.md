---
title: UI overhaul - hierarchy, role-aware views and run workflows
approved: 2026-11-10
---

# UI overhaul: hierarchy, role-aware views and run workflows

## Destination

A signed-in user lands on a view of the projects and jobs they may use, laid out as the project tree, and can find, run, follow and (with the right role) author jobs from it. What a user may do is visible in the controls, and what they may not see is never hinted at. The UI stays the embedded SvelteKit SPA on the generated API client (ADR-0019).

- G1: After login, a user sees every project and job they hold a role on, nested by project path, without first selecting a project.
- G2: Nothing the caller may not see is revealed by the UI: counts, "has children" flags, breadcrumbs, search results and error pages are computed over the visible set, and "no role" looks the same as "does not exist".
- G3: Controls match the caller's role: actions the role allows are usable, actions it does not are disabled with the missing role named or are absent for admin-only areas.
- G4: A user with `runner` can start a job from a typed, validated form and follow the run live (graph, logs, per-step detail) without leaving the page.
- G5: A user with `editor` can create a job and add a version from the UI, with server-side validation, a server-built graph and a version diff.
- G6: An admin can see why a user has, or lacks, a role on a project or job, and which rule granted it.
- G7: The audit, nodes, agents, secrets and accounts pages are usable with realistic data (readable actors, filters, health, copy-reference).
- G8: The UI is usable by keyboard and at phone width.

## Milestones

### M1: Navigable and correct
Exit: signed in as each of the seven dev test users, the tree shows exactly the projects and jobs the documented isolation matrix allows, and a job opens from it with controls that match the user's role.

| Slice | Kind | Delivers | Covers | Blocked by | Verify | Risk | Status |
|-------|------|----------|--------|------------|--------|------|--------|
| R1 | build | Role-aware controls on the job page and a friendly not-found page | G2, G3 | none | e2e: a viewer sees Run disabled with "needs runner" and no schedule or webhook forms; a user with no role gets the not-found page with links home and no hint the job exists | low | |
| R2 | build | Run graph lays out without nodes overlapping | G4 | none | layout test: no overlapping node boxes for graphs of 1 to 20 steps with retry, `when` and status badges | low | |
| R3 | spike | Project tree response shape, visible-only counts and one grouped query for failures, and a bounded lookup of jobs a user reaches only through job-level rules, measured on SQLite and Postgres | G1, G2 | none | ADR recorded; both queries run under 100 ms on 10 000 runs and 1 000 job-rule rows in both databases; throwaway code removed | high | shipped 2026-11-10 ui-r3-tree-spike |
| R4 | build | Server-pruned project tree API and a sidebar tree with jobs under each project, lazy per level, role badge per project, and a "Shared with me" section for jobs reached only through job rules | G1, G2 | R3 (shape and cost) | e2e as four users: exact expected tree, no ancestor stub for a user with a role only on `acme/payments`, a job-rule-only user sees the job under "Shared with me" and no project node, `make gen` clean | high | shipped 2026-11-10 ui-r4-tree |
| R5 | build | Jobs and Projects pages open from the tree with a breadcrumb and a scope selector ("All", "this project and below"); replaces the flat jobs list | G1 | R4 (tree) | e2e: an admin sees all five seeded jobs; selecting `acme` lists `acme` and `acme/payments` jobs only | med | shipped 2026-11-10 ui-r5-jobs-scope |

### M2: Run and follow
Exit: a runner starts `deploy-web` from a validated form and watches it finish with logs and per-step status.

| Slice | Kind | Delivers | Covers | Blocked by | Verify | Risk | Status |
|-------|------|----------|--------|------------|--------|------|--------|
| R6 | build | Run view log viewer: the logs endpoint streamed live, ANSI colours, virtualized, de-duplicated by sequence, bounded rendering (no log viewer exists today) | G4 | none | e2e: a running step's lines appear live; a 100 000 line log renders without exceeding the cap; a reload resumes from the last sequence | med | |
| R15 | build | Log filters by level and step, and a per-step detail panel on the run page | G4 | R6 (log viewer) | e2e: filtering by step shows only that step's lines; the panel shows attempts and status | med | |
| R7 | build | Typed run dialog with per-field server validation errors | G4 | R1 (role-aware controls) | e2e: an invalid option shows its error beside the field and no run is created | med | |
| R8 | build | Home page: recent runs and failures across visible projects, with entry points into the tree | G1 | R4 (tree) | e2e: frank sees an empty state; bob sees runs only from `acme` and `acme/payments` | low | |
| R9 | build | Counts and 24 hour failure roll-ups on tree nodes over visible descendants | G1, G2 | R4 (tree), R3 (query) | e2e: counts differ between bob and dave and match each one's visible set; no double counting | med | |

### M3: Author
Exit: an editor creates a job and a second version from the UI and compares them.

| Slice | Kind | Delivers | Covers | Blocked by | Verify | Risk | Status |
|-------|------|----------|--------|------------|--------|------|--------|
| R10 | build | Create a job and add a version in the UI with on-demand server validation and the server-built graph beside the editor | G5 | R1 (role-aware controls), R5 (tree context) | e2e: erin creates a job in `acme/payments`; a spec error points at its path; a files-managed job is read-only with the reason | med | |
| R11 | build | Version history with a diff between two versions; a run shows the version it ran | G5 | R10 (editor) | e2e: two versions listed, diff shows the changed step, run page names its version | low | |

### M4: Explain and operate
Exit: an admin answers "why can this user run that?" in the UI, and the admin pages hold up with realistic data.

| Slice | Kind | Delivers | Covers | Blocked by | Verify | Risk | Status |
|-------|------|----------|--------|------------|--------|------|--------|
| R12 | build | Read-only effective-access view: role on a project or job for entered groups and an optional email, plus a "my access" view for any signed-in user, with the granting rule and its source (direct, inherited, claim) | G6 | R4 (tree) | e2e: for bob on `acme/payments` it names the inherited `acme` rule; evaluator is the one used for enforcement; the check is audited | med | |
| R13 | build | Audit page: readable actors, filters for action, actor, time and project, system rows hidden by default | G7 | none | e2e: filtering by actor returns only that actor; dispatch rows hidden until toggled | low | |
| R14 | build | Nodes selector builder with saved selectors, agents health and drain, secrets copy-reference, on the existing instance-admin pages | G7 | none | e2e per page, listed in the slice's plan; no secret value is ever rendered | med | refine |

## Open decisions
None open. Project-scoped secrets, nodes and schedules are a separate effort (see Out of scope and Decisions so far).

## Fog
- Phone-width layout and an accessibility review of the tree and run view (G8): sharpen after R5.
- Command palette, pinned and recent items, saved filters, a step timeline, per-step history.
- Global schedules list with next-run preview.
- Guided access-rule editor.
- Per-project tabs on a project's page (secrets, nodes, schedules, webhooks, access) once the project-scoped resources work lands; no new roles are needed, only project-level functions and their UI.

## Out of scope
- Changing the UI stack; the SPA stays Svelte 5, Tailwind v4 and shadcn-svelte.
- Plugin-specific UI.
- Dragging nodes to author a graph.
- Engine and spec features that need design first: retry from a step, approvals, sub-jobs, job hints.
- Non-UI product gaps (notifications, concurrency policy, Git sync, calendars); these get their own roadmap.
- Project-level and site-wide secrets, nodes, schedules and webhooks with isolation, and delegated access granting; needs an ADR and its own roadmap (issue "Project-level and site-wide secrets, nodes, schedules and webhooks"). Service accounts stay instance-admin only.
- Acting as another user (impersonation); needs an ADR.

## Decisions so far
- 2026-11-10 Decision: the UI stays the embedded SPA on the generated API client; no template-driven pages in the app.
- 2026-11-10 Proposal, not yet an ADR: no ancestor stubs, visible-only counts, one response for "no role" and "does not exist" (see the tree issue).
- 2026-11-10 Decision D1: tree nodes show a job count and failed runs in the last 24 hours over visible descendants, with running runs as a separate small count; the R3 spike confirms the cost.
- 2026-11-10 Decision D2: jobs reached only through a job-level rule appear in a "Shared with me" section outside the tree, with no project node or ancestor shown.
- 2026-11-10 Answered D3: no UI code calls the logs endpoint (only the generated schema mentions it), so the run page has no log viewer; R6 builds one and R15 adds filters.
- 2026-11-10 Decision D4: the access view takes groups and an optional email, plus a "my access" view; no acting as another user and no raw claims.
- 2026-11-10 Decision D5: the maintainer wants project-level and site-wide secrets, nodes and schedules with isolation, accounts admin-only, and project admins able to grant access; this is backend work with an ADR, split out of this roadmap. R14 improves the existing instance-admin pages only.
- 2026-11-10 Decision: no new roles, including no site-wide auditor; the four project roles and instance admin are sufficient, and the remaining work is project-level functions and their UI.

## Changes
- 2026-11-10 R3 shipped (ui-r3-tree-spike): ADR-0075 (Proposed) fixes the tree shape and visibility rule; both queries measured at 50 ms or less on SQLite and Postgres. Open points: docs/issues/adr-0075-tree-api-open-points.md.
- 2026-11-10 R4 shipped (ui-r4-tree): `GET /api/v1/tree` and `/tree/shared`, sidebar tree, OIDC e2e as five users; ADR-0075 accepted. Follow-ups: docs/issues/tree-ui-and-api-follow-ups.md.
- 2026-11-10 R5 shipped (ui-r5-jobs-scope): `GET /api/v1/tree/jobs`, `/jobs` and `/projects/<path>` with breadcrumb and scope selector. Follow-ups: docs/issues/jobs-view-follow-ups.md.
