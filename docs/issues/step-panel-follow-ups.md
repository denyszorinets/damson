---
title: Step panel and log filter follow-ups from the R15 audit
type: task
status: open
source: ui-r15-step-panel
---

## Context

Findings of the R15 audit. R15 reads "filter by step" as choosing a step (a log is per execution); a merged run-wide log with a step filter would need a run-level log endpoint.

## Problem

- StepPanel shows the previous step's status and attempts under the new path until the fetch returns; key it by path or reset `step`.
- A step missing from the response shows a heading only; say "Step not found".
- `stamp` omits execution fields, so a running attempt's times and agent can stay stale.
- The level filter resets to "all" when the attempt or step changes (the viewer remounts); keep it in the page.
- With a filter on, "N lines" is the unfiltered total and the "earlier not kept" text counts unfiltered evictions; show "n of total".
- The filter is the stream, not a level: rename to "stream". Active state is a weak contrast cue; counts are not announced (aria-live); the attempt "Log" buttons share one name (label "Open log of attempt N"); the attempts table has no caption or `th scope`.
- Decide whether a run-wide log (all steps merged, filter by step) is wanted.

## Acceptance

- Each point fixed or dropped with a reason.
