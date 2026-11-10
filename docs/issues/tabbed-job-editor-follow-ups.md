---
title: Tabbed job editor follow-ups
type: improvement
status: open
source: pf-r5-tabbed-editor
---

## Context

Findings from the audit of the tabbed job editor (R5) that did not block it.

## Problem

- A refused option name leaves the input showing the refused text and the error stays until the next name edit (`SpecOptions.svelte`).
- Step JSON-box errors are keyed by step index and are not cleared when a step is removed or moved (`SpecSteps.svelte`); the `<details>` open state also follows the position.
- Invalid `with` JSON is reported only on the Steps tab; Validate and Save then use the old value.
- Whitespace-only retry attempts or numeric defaults become `0`; an empty-string default is dropped; a non-object `retry` is overwritten; a bare boolean property schema is rewritten to `{}`.
- Raw `<textarea>`, `<select>` and `<label>` are used where `ui/textarea`, `ui/select` and `ui/label` exist (CLAUDE.md frontend rule 10).
- Typed per-plugin `with` forms through `SchemaForm`, with `${{ }}` pass-through, are not built.
- No e2e for the read-only editor of a viewer.

## Acceptance

Each item fixed or dropped with a reason.
