---
title: "Project-first shell R12: audit follow-ups"
type: chore
status: open
source: pf-r12-trigger-authz
---

## Items

- Tests: no service-token case for get, patch or delete schedule or for get or revoke webhook (scope through `subjSchedule` and `subjWebhook`: out-of-scope 404, viewer-token patch 403); no user list with a nonexistent `job_id`; no fire-path test that a schedule naming a secret ref outside the job's project chain makes no secret usable (covered today only by `internal/secrets` `Authorize` tests).
- A scoped service token must pass `job_id` or `project` when listing; ADR-0076 said it would use its scope. Decide whether tokens need an implicit project.
- On create, a viewer token on an out-of-scope or missing job gets 404 where every other route gives 403 first (the check runs in the handler). Align or document.
- `target.go` repeats the project validate-and-lookup block for `subjTriggerList` and `subjProjectPath`; `fillJobProject` is duplicated per store.
- Instance admins list an empty set for a missing `job_id` or junk `project`; note it in the ADR compatibility paragraph.
