---
title: "Project-first shell R13: audit follow-ups"
type: chore
status: open
source: pf-r13-trigger-tabs
---

## Items

- `act()` reloads without a signal while the 15 s poll may be in flight, so a stale poll result can overwrite the list after a pause or delete until the next poll; serialize the two.
- When the 200-row cap errors the table still says "No schedules" under the alert; hide the table on error.
- `fetchProjectJobs` is one page, so job names past it show the raw id and link under the project path.
- A bearer-token operator who types the tab URL gets a 401 alert (the nav hides the tab); untested.
- A user with only a job rule gets controls on the job page but not on the project tab (the project role is all the tab has).
