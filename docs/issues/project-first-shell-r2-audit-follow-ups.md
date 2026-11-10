---
title: "Project-first shell R2: audit follow-ups"
type: chore
status: open
source: pf-r2-project-table
---

## Items

- Bug: the 10 s poll in `ProjectTable.svelte` replaces the table with page 1, so rows loaded with "More…" vanish within 10 s. Refresh only the first page and keep later pages, or pause polling after More.
- Cost: `LatestRunsByJobAndStatus` windows over every run of every job in the project and runs for each page of jobs. An index on `runs (job_id, status, id)` (a migration in both dialects) plus per-job correlated `ORDER BY id DESC LIMIT 1` lookups bounds it to O(jobs x seeks); a migration needs architect review. Also scope the query to the page's job ids.
- Test gaps: `storetest` `JobRunStats` archives no job and has no canceled-never-started run to pin the nil duration in the database; no e2e assertion for "never run".
- Docs: ADR-0075 (lines 44-45) names only the last status and time; add the success, failure and duration fields and the query.
- Any fetch error on the project table reads "Not found, or you may not have access"; network and 5xx errors should say so.
- Child project rows show "-" in the last-run columns; a roll-up of last failure could fill them.
