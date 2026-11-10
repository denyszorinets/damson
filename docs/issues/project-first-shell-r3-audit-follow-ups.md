---
title: "Project-first shell R3: audit follow-ups"
type: chore
status: open
source: pf-r3-project-activity
---

## Items

- A scoped service token cannot use `GET /runs?project=`: the gate resolves a job-less list to `tgtAll`, which a scoped token fails (safe, but the filter is unusable for it). Resolve `project` to a found target, or document that it needs an unscoped token. Pin it with a test, together with an instance-admin and an unscoped-token case for `?project=`.
- Unrestricted callers (instance admin, login off) that pass `project` use the bounded `visibleRuns` scan; a store-level project filter would serve them in one query and avoid empty pages with a cursor.
- Overview ignores `next_cursor`: a sparse project on a busy instance can read "No runs yet" or a rate over fewer than 50 runs. Activity already hints "load more".
- `fetchProjectJobs` loads one page of jobs, so the Activity job filter and run-row names are incomplete in a project with many jobs.
- After a failed "More" on Activity the error replaces the loaded list.
- `RUN_STATUSES` repeats the server enum; derive it from the generated schema.
- `projectOfJobRow` returning `errNoParent` maps to a non-404 status in the job plus project path (admin or login off only).
