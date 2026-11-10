---
title: "Project-first shell R1: audit follow-ups"
type: chore
status: open
source: pf-r1-app-shell
---

## Context

Should and Nice findings from the R1 audit (roadmap `docs/roadmap/ui-project-first.md`). None blocked the slice.

## Items

- Dead code left by R1: `crumbs` in `web/src/lib/jobs.ts`; `fetchRecentRuns` and `jobsById` in `web/src/lib/runs.ts`; the `lib/tree.ts` expanded-state helpers. R11 deletes them.
- The project switcher is a plain select, not filterable; the plan said filterable (R10).
- Token operators get no switcher and Home only points to `/projects`.
- The breadcrumb links every ancestor in the URL, including one a caller with a role on a child alone cannot open.
- Project hrefs are built by hand on the job and new-job pages without `projectRest`; a path with a `-` segment would misparse (R4 moves them).
- `JobsView` shows "Not found, or you may not have access" for any load error, including network and 5xx.
- The switcher's project list loads once and is never refreshed.
- No e2e check compares the switcher set with the Home set for the same user.
- Home has a Role column the plan did not list; the child column is yes/none until a count exists.
