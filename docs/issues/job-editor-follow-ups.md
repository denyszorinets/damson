---
title: Job editor follow-ups from the R10 audit
type: task
status: open
source: ui-r10-job-editor
---

## Context

Should and Nice findings of the R10 audit.

## Problem

- Stale errors stay on screen after the user edits the text; `oninput` clears `checked` but not `issues`.
- A 409 on saving a version has no way forward: `current_version` is fixed at load, so each retry sends the same `expected_version`. Offer "Reload latest" and say what happened.
- The edit page's `.catch` lacks the `jobId !== id` guard that `.then` has, so a slow load of job A can show its error on job B.
- `loadForEdit` uses `JSON.stringify` of the stored spec: key order and big-integer precision may differ from the stored bytes; check `engine.Checksum` canonicalisation and that saving unchanged text does not create a spurious version.
- e2e: `getByText('version 2')` may match several elements; `toHaveURL(/\/jobs\/[^/]+$/)` also matches `/jobs/new`; the tests rely on projects and jobs made by earlier tests.
- a11y: link the textarea to the issues list (`aria-describedby`), give the valid line `role="status"`; reset `busy` in `finally`; `fetchProject` hides network failures as "no New job".
- Confirm the server also refuses new versions on archived and files-managed jobs.

## Acceptance

- Each point fixed or dropped with a reason.
