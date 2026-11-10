---
title: Project pages, Jenkins-style overview and ACL-filtered nodes
approved: 2026-11-10
---

# Project pages, Jenkins-style overview and ACL-filtered nodes

Continues `docs/roadmap/ui-project-first.md` (all slices shipped). Raised by the maintainer while checking the shipped UI (2026-11-10).

## Destination

A project page tells its team what is running and what failed, and nodes become a project resource: a group is given a named, filtered set of nodes through the same access rules that give it a role, and sees and runs only those. The REST API and the existing routes (`/projects`, `/jobs`, `/api/v1/...`) stay; this roadmap adds to them.

- G1: On `/projects` every project row opens the project, and the page reads as project management (create in a modal, delete), not as a second Home.
- G2: A project's Overview shows what is running now, what is waiting, the latest runs and the health of each job, as Jenkins' folder and job views do.
- G3: A project has a Nodes tab listing the nodes the caller may see for that project, with the sets that grant each; it is not admin-only.
- G4: A group can own a filtered node set: access rules reference named node sets (a CEL filter over the node's name, tags, attributes, pool and os; a regex is one expression), the caller's visible nodes are the union of the sets of the rules that match them, a node outside it is a 404 as missing, and every change is audited.
- G5: What a caller may target follows their node sets at trigger and dispatch, inside the project's existing `node_scope` ceiling, on SQLite and Postgres, with the same response for hidden and missing nodes.
- G6: A project admin edits node sets and attaches them to rules in the UI, with a preview of the nodes a filter matches before saving.

## Milestones

### M1: A project page that shows the work
Exit: from `/projects` a user opens a project, whose Overview lists running and waiting runs, recent runs and per-job health, for the dev test users, with the isolation matrix unchanged.

| Slice | Kind | Delivers | Covers | Blocked by | Verify | Risk | Status |
|-------|------|----------|--------|------------|--------|------|--------|
| R1 | build | `/projects` redesigned as project management: a table whose rows open the project (name, description, child projects, jobs, your role), search, and a New project button that opens a modal (shadcn dialog added to `ui/`) with the same fields and server errors shown in it; delete stays per row for admins; the token entry and node scope controls are kept; no route removed | G1 | none | e2e: from `/projects` click `acme` and land on its page; New project opens a modal, creates `acme/new`, closes and lists it; a bad path shows the error in the modal; delete still works | low | |
| R2 | build | Overview tab: Running now and Waiting blocks (project runs filtered by `running` and `pending`, refreshed by polling), recent runs, per-job health (last result and success rate); only jobs and runs the caller may see | G2 | none | e2e: start a run in `acme/payments`, Overview of `acme` shows it under Running now with a link; carol (viewer) sees the same; erin sees nothing of `acme` | med  shipped 2026-11-10 ui-pf2-r2-overview |

### M2: Nodes governed by access rules
Exit: a member of `acme-ops` sees only the nodes of their set on the `acme` Nodes tab and can target only those, a member of no set sees none, and an instance admin sees all.

| Slice | Kind | Delivers | Covers | Blocked by | Verify | Risk | Status |
|-------|------|----------|--------|------------|--------|------|--------|
| R3 | spike | ADR-0078 by `architect`: node sets (separate named object referenced by rules, or a field on rules), visible-set semantics (union, ceiling), 404 rules, how triggers, schedules and webhooks use it (D1), audit, schema and migration for both databases | G4, G5 | none | ADR accepted; D1 and D2 answered; the Nodes tab and its API read-only shape named | high | |
| R4 | build | Backend: node sets stored and validated (CEL compiled on write, cost-limited), referenced by access rules, visible set computed per caller; `GET /projects/{path}/nodes` lists only visible nodes with their granting sets; set CRUD by project admin; audit; tests on SQLite and Postgres including 404 for hidden nodes | G3, G4 | R3 (ADR) | API tests on both databases: bob sees only his set, a caller with no matching rule gets 404, instance admin unchanged, no credential or host key in any response | high | |
| R5 | build | Trigger and dispatch honour the caller's visible set (intersection with the job selector and the project `node_scope`); scheduled and webhook runs per D1; run targets frozen as today | G5 | R4 | engine and API tests: a run by bob targets only his nodes; a selector matching a hidden node yields none of it; dispatch of a hidden literal `node:` fails as `unknown node` | high | |
| R6 | build | Project Nodes tab: table of visible nodes (name, address, tags, pool, os, enabled) with filter box and a "visible through" column naming the sets; read-only | G3 | R4 (read API), R3 shipped sidebar | e2e: bob lists his set on `acme`; carol sees her set; frank has no Nodes tab and `/projects/acme/-/nodes` is a not-found view | med | |
| R7 | build | Access tab: node set editor (name, regex helper that writes CEL, raw CEL, live preview of matching nodes) and a node-set picker on each rule; project admin only | G4, G6 | R4, R6 | e2e: grace (admin of `acme`) creates "prod web" with a regex, sees the preview, attaches it to the `acme-ops` rule; bob then sees only those nodes; a bad expression is refused with its error and nothing is saved | med | |

## Open decisions
- D1: Do scheduled and webhook runs use the filter of the rule that granted their creator's role (evaluated when the schedule or hook is saved), or the full project scope? Default: the creator's sets, frozen at save. Answer by: `architect` in R3. Blocks R5.
- D2: Node sets as a separate named object that rules reference, or a node filter field on each rule? Default: separate named object (shared by groups and projects, edited in one place). Answer by: `architect` in R3. Blocks R4.

## Fog
- Writing nodes (create, edit, delete) by project editors or admins, and project-owned nodes instead of the global inventory.
- Exclusive ownership (a node reserved to one group); this roadmap is access only, a node can be in many sets.
- Dynamic or tag-driven set membership beyond a CEL filter.

## Out of scope
- New roles, or changing role meanings.
- Removing the `/projects` page, the `/jobs` route or any REST API route.
- Service-account nodes; agent pools.

## Changes
- 2026-11-10 R2 shipped: Overview has Running now, Waiting, job health and recent runs; verified on the OIDC stack with bob (runner) and erin, carol not exercised.

## Decisions so far
- 2026-11-10 Decision: node access follows the access rules (maintainer): a group "owns" a filtered node set; the project `node_scope` stays as the hard ceiling and sets can only narrow it.
- 2026-11-10 Decision: CEL stays the only filter language (ADR-0012); a regex is `node.name.matches(...)` and the UI offers a regex helper that writes it.
- 2026-11-10 Decision: nodes are visible per project and per caller; a hidden node answers as a missing one.
- 2026-11-10 Decision: `/projects` gets row click-through, a cleaner layout and a modal New project form (maintainer); R1 no longer needs refining.
