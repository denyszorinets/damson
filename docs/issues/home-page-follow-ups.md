---
title: Home page follow-ups from the R8 audit
type: task
status: open
source: ui-r8-home
---

## Context

Should and Nice findings of the R8 audit.

## Problem

- The e2e wait counts any three failed runs in the newest 25, so it can pass before the three runs it created have failed; keep the run ids and poll `GET /runs/{id}` for each.
- `jobsById` fetches `/tree/jobs?limit=200` and then one `GET /jobs/{id}` per other job, sequentially, every 10 seconds; cache id to name across ticks and fetch the missing ones together, or return job name and project on the run list.
- A failed tick replaces the whole page with the alert; keep the old data and show the alert above it.
- `jobsById` ignores non-throwing failures from the two endpoints (labels fall back to the raw job id).
- `visibleRuns` scans up to 20 pages of 200 runs per request for non-admin users, every 10 seconds; a user who sees few runs among many gets a partial list (the page says so).
- "N recent failed" counts only the newest 25 runs; say so. The skeleton has no `aria-busy`.

## Acceptance

- Each point fixed or dropped with a reason.
