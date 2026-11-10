---
title: Run dialog follow-ups from the R7 audit
type: task
status: open
source: ui-r7-run-dialog
---

## Context

Should and Nice findings of the R7 audit.

## Problem

- The e2e "no run created" check for the mocked 422 is vacuous (the route answers, the server never sees the request) and its before/after count can race with other tests; assert the intercepted POST count and use a distinct job name per test.
- The client-refusal test asserts Ajv's wording "must match pattern" and the sjsf default submit label; assert an error element near the field instead.
- A nested option error such as `options.tags.0` is shown on the `tags` field, not the item.
- A server message stays under a field after the user edits it (the form validates on submit only); clear it with `updateFieldErrorsByPath` on change.
- `SchemaForm` is not told `busy`, so the submit button can be pressed twice during the request.
- `SecretOptionViolations` locations were not checked against `options.<name>`.

## Acceptance

- Each point fixed or dropped with a reason.
