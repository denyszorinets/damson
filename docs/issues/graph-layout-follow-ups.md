---
title: Graph layout follow-ups from the R2 audit
type: bug
status: open
source: ui-r2-graph-layout
---

## Context

Findings of the R2 audit. R2 sized step boxes by their badges; containers were out of its scope.

## Problem

- Group, catch and finally headers are a fixed 36px, but `GraphBox` renders their badges (for each, when, retry, continue on error) in the header; a group with badges can grow past 36px and cover its first inner step.
- `GraphBox` renders an empty badge span even with no badges, adding a 4px gap that `stepHeight` does not count (absorbed by `gapY`).
- The wrap model ignores the 2px border (use `room - 2`) and uses a fixed 6.6px per character; wide fonts could wrap earlier.
- The e2e only fails when the estimate is off by more than `gapY`; add a case with a group, a plugin line and long text. Its `scrollHeight > clientHeight` clip check cannot fail on an auto-height box; drop or replace it.
- The keyed each `(b.text)` could collide if a status equals a badge prefix; key by `testid ?? text`.

## Acceptance

- Each point fixed or dropped with a reason.
