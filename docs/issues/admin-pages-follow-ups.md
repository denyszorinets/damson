---
title: Admin pages follow-ups from the R14 audit
type: task
status: open
source: ui-r14-admin-pages
---

## Context

Should and Nice findings of the R14 audit.

## Problem

- `STALE_AFTER_MS` is 30 s, but `last_seen` is written by a 10 s batch flush and the server's own liveness window is 40 s; a healthy agent can look stale. Use 40 s or more, or tie it to the server constant.
- Health compares the server's `last_seen` with the browser clock; a skewed browser marks agents stale. Derive an offset from the response `Date` header or return a server `now` or a computed health.
- A refused builder clause (empty value, attribute key that is not an identifier) gives no feedback; add an inline error or `aria-invalid`. Keys with `-` or `.` could use `node.attributes['k']`.
- Saved selectors use one global localStorage key, so users of a shared browser see each other's (they can name tags and nodes); say so in the docs or clear the key on logout.
- Add a Go test that `clause()` output compiles through `expr.NewNodeScopeEnv` (a value with a quote or backslash).
- The "stale" tooltip is not keyboard reachable; put it in visible text or `aria-label`. The copy-failure message reuses the page `error` state.

## Acceptance

- Each point fixed or dropped with a reason.
