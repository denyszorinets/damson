---
title: "Project-first UI: final review follow-ups"
type: chore
status: open
source: ui-project-first final review
---

## Items

- Dead code left by the run: `fetchRecentRuns` and `jobsById` in `web/src/lib/runs.ts`; `loadExpanded`, `saveExpanded` and `ancestry` in `web/src/lib/tree.ts` (only `tree.test.ts` uses them); `fetchJobs` in `web/src/lib/jobs.ts`; `crumbs` in `web/src/lib/jobs.ts`. R11 deletes them.
- Internal links still go through the old `/jobs/[id]` and `/runs/[id]` redirects: after creating or saving a job (`jobs/new`, `jobs/[id]/edit`), the run page job-version link without a project, Home shared jobs, `RunList` and `RunDialog` fallbacks. Link to the project-first URL where the project is known.
- `ProjectTriggers` and `ProjectSecrets` use only the session credential; a bearer-token operator who types the tab URL sees a raw 401 error with no way to enter a token.
- `fetchProjectJobs` reads one page of jobs, so runs, schedules and webhooks of later jobs show an id until the page is raised or paged.
- `NotFound` and the Home token message still link to the old `/projects` index, a second project list beside Home; decide its fate with R11.
