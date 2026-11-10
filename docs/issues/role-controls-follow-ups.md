---
title: Role controls follow-ups from the R1 audit
type: task
status: open
source: ui-r1-role-controls
---

## Context

Nice findings of the R1 audit.

## Problem

- `isInstanceAdmin()` is true for `kind === 'service_account'`; a service-token viewer would see trigger forms that answer 403. Latent: the UI does not use service tokens.
- A disabled Run button's `title` is not reachable by keyboard or screen reader; add `aria-describedby` to the "needs runner" span.
- `getMe()` is a plain variable, not reactive; `isInstanceAdmin()` relies on `setMe` running before the page renders.
- The no-role e2e test names a second erin context `gone`; rename.

## Acceptance

- Each point fixed or dropped with a reason.
