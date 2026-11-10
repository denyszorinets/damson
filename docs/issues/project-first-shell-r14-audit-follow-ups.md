---
title: "Project-first shell R14: audit follow-ups"
type: chore
status: open
source: pf-r14-secrets-authz
---

## Items

- Decide whether project admins should see `secret.put` and `secret.delete` audit rows for their scopes. `GET /audit` sets `HideSecrets` for everyone but an instance admin, so ADR-0077's "project admins read their events through the audit list" does not hold for secret events today; hiding is the safe default.
- A real job-rule editor and a viewer token out of scope are not exercised in the secrets tests; add them with a Postgres run in CI.
- Body parsing now runs before authorization for any logged-in user on `PUT /secrets/...` (harmless: it is their own value).
