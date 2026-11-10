---
title: ADR-0075 tree API open points (archived jobs, plan, admin cache, truncated)
type: task
status: open
source: ui-r3-tree-spike
---

## Context

Found by the R3 audit; settle them in R4 or R9 while building the tree API.

## Problem

- The jobs count and roll-up queries ignore `jobs.archived_at` (migration 00023); decide whether archived jobs and their runs count.
- The grouped runs query ORs `status='failed'` with `status='running'`; check the plan uses `runs_finished_idx` and `runs_status_idx` at scale, not only at 10 000 runs.
- `projectRoles` caches only for non-admin sessions; admins and service accounts go through `roleOn` uncached. Say so in the ADR.
- The ADR says `NormalizeTime` fixes the width of SQLite timestamps; it only truncates to microseconds in UTC.
- `truncated` is not placed in any response shape; name it on `/tree/shared`, and say whether `/tree` has it.

## Acceptance

- ADR-0075 or the R4/R9 plans state each point.
