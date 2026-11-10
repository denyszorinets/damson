---
title: Version history follow-ups from the R11 audit
type: task
status: open
source: ui-r11-versions
---

## Context

Should and Nice findings of the R11 audit.

## Problem

- A job with more than 200 versions lists only 1 to 200: the current version is missing from the table and selects. Follow `next_cursor` or fetch the tail, or say "showing the first 200".
- A failed compare leaves the previous diff on screen under the error; clear `diff` and `tooBig` first, and caption the diff "Changes from v{a} to v{b}".
- The diff `<pre>` scrolls without `tabindex`, role or label; unchanged lines have low contrast on `bg-muted/30`.
- "Identical" is shown when only formatting or key order differs while the checksums differ; say "identical content".
- Trim the common prefix and suffix before the LCS so the 2000-line cap rarely bites.
- The run page fetches the whole `GET /runs/{id}` only for `job_version`; add `job_version` (and `job_id`) to the run graph or `Run` response. A failed request silently hides the badge.
- Confirm the path-prefix authz covers `get-job-version` (viewer on the job).

## Acceptance

- Each point fixed or dropped with a reason.
