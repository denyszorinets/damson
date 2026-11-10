---
title: Tree counts follow-ups from the R9 audit
type: task
status: open
source: ui-r9-tree-counts
---

## Context

Should and Nice findings of the R9 audit.

## Problem

- `testProjectCounts` never archives a job, so removing `j.archived_at IS NULL` from either query would pass; archive one job with a failed run and assert the counts drop.
- The no-double-count API test gives the parent no jobs or runs of its own; give `team` its own job and failed run and assert `team = own + web`.
- `(status = 'failed' AND finished_at >= ?) OR status = 'running'` probably cannot use `runs_finished_idx` for the running branch, so each `/tree` request may scan `runs` (cost grows with history, paid on every lazy level). Run EXPLAIN at 100 000 runs on both databases; consider a UNION of two selects and a status index, or cache counts for a few seconds.
- The e2e `before + 1` count is racy against other tests; use `>=` and an explicit poll timeout.
- `rollups` runs even when the page is empty or the parent is not visible; skip it then. The two `ProjectCounts` bodies could share a helper.
- Counts are not refreshed after a level loads.

## Acceptance

- Each point fixed or dropped with a reason.
