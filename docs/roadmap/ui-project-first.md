---
title: UI project-first navigation - enter a project to see its jobs
approved: 2026-11-10
---

# UI project-first navigation

Supersedes the navigation of `docs/roadmap/ui-overhaul.md` (its G1: "see every project and job without first selecting a project", and decision D2). The role-aware controls, run page, log viewer, job editor and audit work of that roadmap stay; this roadmap changes where they live.

## Destination

The project hierarchy is the structure of the UI. Home lists projects, never jobs. To see a job a user enters its project, as in Rundeck, and a project page shows its child projects and jobs together as folders do in Jenkins. Everything about a project (jobs, activity, secrets, access, settings) sits behind that project's own sidebar and breadcrumb. Instance-wide tools live in one Admin area.

References checked on 2026-11-10: Rundeck (project switcher, per-project rail of Dashboard, Jobs, Nodes, Activity, Project Settings; job page with run form and tabs; instance settings behind a gear menu) and Jenkins (folder tables mixing folders and jobs with last success, last failure and duration; context sidebar; breadcrumb; per-folder credentials showing inherited entries; Manage Jenkins separate).

- G1: No screen lists jobs across projects. Home lists projects the caller holds a role on; jobs appear only inside their project, plus one "Shared with me" entry for jobs reached only through a job rule.
- G2: The hierarchy is navigable like folders: a project page shows child projects and jobs in one table with last-run status, a breadcrumb shows the path, and a top-left switcher jumps to any visible project.
- G3: Each project has its own sidebar (Overview, Jobs, Activity, Secrets, Access, Settings); every page in it shows only that project's data.
- G4: A job page holds everything about the job in tabs: run form, runs, definition and versions, schedule, webhooks; the job editor is tabbed the same way.
- G5: Instance-wide pages (agents, audit, nodes, accounts, all secrets, access check, log purge) are in an Admin area shown only to instance admins.
- G6: URLs carry the project path; old URLs redirect; the isolation matrix of the previous roadmap still holds (no revealed counts, flags or names; no role looks like missing).
- G7: The shell is usable by keyboard and at phone width.

## Milestones

### M1: Enter a project
Exit: signed in as the dev test users, home shows only projects, choosing one opens its page with child projects and jobs in one table and a breadcrumb, `/jobs` no longer exists as a list, and the isolation matrix result is unchanged.

| Slice | Kind | Delivers | Covers | Blocked by | Verify | Risk | Status |
|-------|------|----------|--------|------------|--------|------|--------|
| R1 | build | App shell: top bar with project switcher and path breadcrumb; route scheme `/projects/<path>/-/<tab>`; Home as a projects table (name, child count, jobs, failed in 24 h from the existing counts); old tree sidebar and `/jobs` list removed from navigation | G1, G2, G6 | none | e2e: bob's home lists only his projects and no job; switcher lists the same set; `/jobs` redirects to home | med shipped 2026-11-10 pf-r1-app-shell |
| R2 | build | Project page table: child projects and jobs together with status, last success, last failure and duration per job (additive API fields on the jobs-in-project read) | G2, G3 | R1 (shell and routes) | e2e: `acme` shows its child projects and jobs with last-run columns; a user with a job-only rule sees none of them | med | |
| R3 | build | Project sidebar and Overview and Activity tabs: recent runs, success rate, run list filtered by status and job, scoped to the project (`GET /runs` gets a visible-set `project` filter) | G3 | R1 (shell and routes) | e2e: Activity of `acme/payments` lists only runs of its jobs; a runner on another project cannot read them (404 as missing) | high | |

### M2: A job and a project are self-contained

Exit: a user runs, inspects and edits a job from its project URL with a tabbed page, and a project admin reaches the project's secrets, access and settings from its sidebar.

| Slice | Kind | Delivers | Covers | Blocked by | Verify | Risk | Status |
|-------|------|----------|--------|------------|--------|------|--------|
| R4 | build | Job page and run page under the project URL with breadcrumb; job page tabs Run, Runs, Definition and versions, Schedule, Webhooks (existing components moved into tabs); old `/jobs/<id>` and `/runs/<id>` redirect | G4, G6 | R2 (project page links), R3 (project sidebar) | e2e: from the project table a job opens at `/projects/<path>/-/jobs/<id>` with tabs; old URL redirects; viewer sees the run form disabled with the missing role | med | |
| R5 | build | Tabbed job editor: Details, Options, Steps, Triggers tabs editing their part of the spec, with the JSON view kept; server validation by path as today | G4 | R4 (job page route) | e2e: edit a step on the Steps tab, save, new version appears in Definition; invalid input is shown on its tab | high | refine |
| R6 | build | Project Settings tab (name, description, delete or archive as the API allows) and Access tab (the project's rules read-only and the effective-access check with the project filled in) | G3 | R3 (project sidebar) | e2e: dave (admin of `acme`) sees Settings and Access; bob (viewer) sees neither tab | med | |
| R12 | build | Project-scoped authorization for schedules and webhooks (backend): a schedule is managed by the roles that may edit its job, a webhook by the roles the ADR names; a caller without a role gets the response for a missing resource; every change audited; role mapping decided in an ADR by `architect` first | G3, G4 | D1 (role mapping) | API tests on SQLite and Postgres: editor of `acme/payments` creates a schedule on its job and gets 404 on another project's; a viewer gets 403 on write and can read; instance admin unchanged | high | |
| R13 | build | Project Schedules and Webhooks tabs (all schedules of the project's jobs with next run and enabled state; webhooks with their jobs) plus the same controls on the job page tab, shown by role | G3, G4 | R12 (authorization), R3 (project sidebar), R4 (job page) | e2e: dave lists and pauses a schedule in his project, bob (viewer) sees them read-only, erin sees a not-found view | med | |
| R14 | build | Project-scoped authorization for secrets (backend): names, create, update and delete of a project's secrets by its roles per D1; a job still uses only its own project's and ancestors' secrets; values never returned; a caller without a role gets the response for a missing resource; every change audited; masking tests for every new path; role mapping decided in an ADR by `architect` first | G3 | D1 (role mapping) | API tests on SQLite and Postgres: admin of `acme/payments` creates and reads the name of a secret there and gets 404 on another project's; editor sees names only; viewer sees nothing; no response or audit row holds a value; a dispatch masking test passes | high | |
| R7 | build | Project Secrets tab: the project's own secrets and the ancestors' it can use, labelled with their origin as Jenkins does, with create, update and delete by role; values never shown or returned | G3 | R14 (authorization), R3 (project sidebar) | e2e: dave (admin of `acme`) creates a secret on `acme/payments` and sees it next to the inherited `acme` one, labelled; no value in the DOM; bob (viewer) has no Secrets tab | med | |

### M3: Admin split and clean-up

Exit: instance tools are only under Admin, the old pages and components are gone, and the shell works by keyboard and on a phone.

| Slice | Kind | Delivers | Covers | Blocked by | Verify | Risk | Status |
|-------|------|----------|--------|------------|--------|------|--------|
| R8 | build | Admin area `/admin` with its own left nav: Agents, Audit, Nodes, Secrets (all scopes), Service accounts, Access check, Log purge; gear entry in the top bar for instance admins only; old top-level routes redirect | G5, G6 | R1 (shell) | e2e: alice sees the gear and every Admin page; bob has no gear and `/admin/audit` is a not-found view | med | |
| R9 | build | Shared with me on Home (jobs reached only through a job rule, no project or ancestor shown) and a project search box on Home (name or `/regex/`) over the visible set | G1 | R1 (Home) | e2e: frank sees one shared job and no project; searching `pay` lists only visible projects | low | |
| R10 | build | Keyboard and phone width for the shell: switcher opens with `/`, table and tab keyboard use, sidebar collapses to a drawer under 640 px | G7 | R4 (all shell parts exist) | e2e: switcher by keyboard; at 390 px width the sidebar is a drawer and no horizontal scroll | low | |
| R11 | build | Contract: delete `ProjectTree`, `TreeNode`, the `/jobs` list, `/projects` index route and dead redirects older than this roadmap; update the UI reference docs and screenshots list | G1, G6 | R5, R6, R7, R8, R9, R10, R12, R13, R14 | `rg` finds none of the removed names; `make check` and the whole e2e suite pass | low | |

## Open decisions

None.

## Fog

- Project-scoped nodes (Rundeck's Nodes rail entry): nodes are an instance inventory and admin-only in the backend; they wait for the backend effort in `docs/issues/project-level-and-site-wide-secrets-nodes-schedules-and-webh.md` (needs `architect` and an ADR) and then become a slice here.
- Project admins granting roles without hand-written rules (guided rule builder): same backend effort.
- Saved views per table (Jenkins view tabs).

## Out of scope

- New roles, a site-wide auditor, or changing the access model.
- Backend project scoping of nodes (own roadmap, own ADR) and a site-wide secret scope. Schedules, webhooks and project secrets are in scope (R12, R14).
- A new visual theme; the existing shadcn components stay.
- Agent drain.

## Decisions so far

- 2026-11-10 Decision: nested project paths are Jenkins-style folders; the routing scheme is `/projects/<path>/-/<tab>` with one catch-all route that reads the path before `/-/`, because SvelteKit allows a rest parameter only last.
- 2026-11-10 Decision: jobs reached only through a job-level rule appear on Home under "Shared with me", with no project or ancestor shown (carried over from the previous roadmap's D2, now the only place jobs appear outside their project).
- 2026-11-10 Decision: new fields (last success, last failure, duration per job; project filter on runs) are additive, computed over the visible set, and never reveal what the caller cannot see.
- 2026-11-10 Decision: schedules and webhooks are project-level, not admin-only (maintainer, comparing Rundeck): they stay on the job as a tab and also list per project; the backend authorization moves to project roles in R12, role mapping per D1.
- 2026-11-10 Decision: the Rundeck and Jenkins references are for layout and flow only; no behaviour is copied beyond what Damson's access model allows.
- 2026-11-10 Decision: project secrets are managed by project roles, not instance admin only (maintainer): R14 moves the authorization, R7 is the tab; role mapping per D1; the rule that a job uses only its own project's and ancestors' secrets is unchanged.
- 2026-11-10 Decision (D1, weak-but-explicit): schedules are managed by `editor` and `admin` of the job's project; webhooks by project `admin`; secrets created, changed and deleted by project `admin`, names visible to `editor`, hidden from `viewer` and `runner`; `viewer` and `runner` read schedules; instance admin everything and the only manager of site-wide secrets. `architect` reviews it in the ADR before R12 and R14. Revisit if `architect` finds an `editor` creating a schedule is privilege escalation.

## Changes

- 2026-11-10 R1 shipped (pf-r1-app-shell).
