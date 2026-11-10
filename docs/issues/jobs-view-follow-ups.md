---
title: Jobs view follow-ups from the R5 audit
type: task
status: open
source: ui-r5-jobs-scope
---

## Context

Should and Nice findings of the R5 audit.

## Problem

- `JobsView.svelte` `more()` has no abort or scope guard: clicking More then another scope before it returns appends old-scope jobs to the new list.
- Any error with a scope set reads "Not found, or you may not have access"; only a 404 should. A failed More hides the loaded table.
- `GET /api/v1/tree/jobs` runs one `ListJobs` query per visible project per page; note the bound or add a store query over many projects.
- `scope-project` is a Button with no action and `aria-pressed`; `aria-pressed` on a link is invalid; the last crumb links to itself (use `aria-current`).
- `list-scoped-jobs` uses `subjHandler`, so a service token's role rank is not checked; harmless at viewer, matters if the role is raised.

## Acceptance

- Each point fixed or dropped with a reason.
