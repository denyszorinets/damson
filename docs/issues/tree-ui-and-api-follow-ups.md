---
title: Tree UI and API follow-ups from the R4 audit
type: task
status: open
source: ui-r4-tree
---

## Context

Should and Nice findings of the R4 audit, plus two test failures seen while verifying R4 that R4 did not cause.

## Problem

- A failed first load of a tree node cannot be retried: collapse and reopen shows the stale error. Add a Retry button.
- A failed "More" on the roots replaces the whole tree with the error; use a separate error.
- `children()` and `hasChildren()` in `internal/api/tree.go` are O(n) per call; build a parent index in `visibleScope`.
- `testListJobRules` has no archived-job case.
- A level is not refreshed after its first load; collapsing a parent drops its children's paging state.
- `TestDevKEKIsGeneratedAndReused` (`internal/cli`) fails on macOS at HEAD (temp path under `/private/var`).
- `plugins/helm` contract `cancel_blocking` failed once under load ("secret leaked into logs") and passed alone: flaky.
- `web/e2e/agents.test.ts` expects `os=linux`; fails on macOS.

## Acceptance

- Each point fixed or dropped with a reason.
