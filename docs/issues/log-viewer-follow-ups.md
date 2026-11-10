---
title: Log viewer follow-ups from the R6 audit
type: task
status: open
source: ui-r6-log-viewer
---

## Context

Findings of the R6 audit. R6's Verify clause "a reload resumes from the last sequence" is unmet: a reload replays from sequence 1.

## Problem

- A page reload replays the whole log and keeps the newest 5000 rows; decide to persist `lastSeq` per execution (sessionStorage) so a reload resumes, or reword the roadmap clause to "a dropped connection resumes".
- `parseAnsi` mishandles extended colours: `38;5;n` and `38;2;r;g;b` are split into separate SGR codes, so `31` or `0` inside them sets red or resets. Skip the 2 or 4 parameters after 38 and 48.
- The cap is by rows, not bytes: 5000 very long lines stay in memory and `parseAnsi` runs per visible row on every render. Truncate the display per row (about 10k characters) or memoize the parse; check the end-to-end line cap.
- `rows` is `$state` (deep proxy) replaced every frame; `$state.raw` fits.
- Payloads from the stream are cast without a guard: a non-number `seq` makes `lastSeq` NaN and stops dedupe; an undefined `line` throws in render.
- The spacer's `min-w-max` has no effect; the programmatic scroll to the bottom can show one blank frame.

## Acceptance

- Each point fixed or dropped with a reason.
