---
id: pf-r10-keyboard-phone
title: "R10: keyboard and phone width for the shell"
lane: s
status: shipping
tracker: null
sources: []
roadmap: docs/roadmap/ui-project-first.md#R10
todo: []
base_ref: 52aec934e1c5ecc263e33fc7ee25b31930f48920
approvals:
  plan: 2026-11-10
verified: "cd web && npm run lint && npm run check && npm run test:unit: pass"
assumptions:
  - "The drawer is a collapsible panel behind a Menu button (aria-expanded), not an animated overlay."
  - The / shortcut presses the trigger opening key through a synthetic ArrowDown keydown because opening bits-ui Select by state alone leaves the list without keyboard focus.
  - Table keyboard use rests on native link focus; no custom grid navigation.
fix: []
defects: []
audit:
  verdict: PASS
  round: 0
  must: []
  fingerprint: 384adfab68607cb775fd7c8245e415a6e7748c1e
  at: 2026-11-10
ship:
  prepared: 2026-11-10
  fingerprint: 5e61a3ef8c4b67332db43f9e9242c62b286c3383
  tree: 9e90ca260c38d01e44e248199fdef6d13be79f5a
blocker: null
---

# R10: keyboard and phone width for the shell

## Goal
The shell is usable by keyboard and at phone width: `/` opens the switcher, arrow keys move through navigation lists, and below 640 px the project sidebar is a drawer with no sideways scroll.

## Seams
- `nextIndex` (unit); the switcher shortcut, nav drawer and no-sideways-scroll against the OIDC users (e2e).

## Steps
1. `lib/keys.ts` (nextIndex, isTyping, arrowNav), switcher shortcut, drawer nav, wrapping top bar and Admin nav → verify: vitest `nextIndex`; svelte-check
2. e2e: "/" opens and chooses, "/" typed into a field is text, arrows/Home/End/Enter on the tabs, at 390 px the drawer and no sideways scroll on Home, project, activity and admin pages, desktop unchanged → verify: e2e-oidc suite, three runs stable
3. Docs and changelog → verify: make docs-check

## Done when
- The switcher opens by keyboard; at 390 px width the sidebar is a drawer and no horizontal scroll.

## Non-goals
- Overlay drawer animation, full table keyboard grids, a filterable switcher.

## Audit

Round 1 FAIL: the / shortcut also fired for a slash typed inside the open switcher list or a dialog. Fixed (ignored when defaultPrevented or inside a listbox or dialog). Round 2 PASS, no Must.
