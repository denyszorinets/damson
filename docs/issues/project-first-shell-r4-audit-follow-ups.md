---
title: "Project-first shell R4: audit follow-ups"
type: chore
status: open
source: pf-r4-job-run-pages
---

## Items

- The old `/jobs/<id>` route treats any failed lookup as not found, and a network error leaves a blank page (no catch). Show the error for a 5xx or offline, not-found only for a 404.
- `RunView` renders the run and opens the stream before the project check finishes, so a run opened under the wrong project briefly shows under the wrong crumb; gate on the project check, and do not swallow a failed `fetchJobProject`.
- Tests: no e2e for a hidden job or run at the new URLs (erin), the old `/runs/<hidden>` fallback, unknown or extra segments on a run URL, a wrong-project run URL, or token mode.
- An unknown job tab shows the job header above the not-found body; the Schedule and Webhooks URLs for a non-admin show "This page was not found" with the nav visible.
- Redirect carries an unvalidated sub-tab to the job project; the old page already showed the job project to a job-rule-only user.
