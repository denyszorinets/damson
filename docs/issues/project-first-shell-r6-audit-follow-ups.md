---
title: "Project-first shell R6: audit follow-ups"
type: chore
status: open
source: pf-r6-settings-access
---

## Items

- Bug: after a 409 on saving the description, the loaded `version` is not refreshed, so every retry fails until reload. Re-read the project on a conflict.
- A11y: announce "Saved." (`role="status"`), move focus into the delete confirmation (Cancel or "Yes, delete", `alertdialog`), label the description with `aria-labelledby`, and clear "Saved." when the text changes again.
- Tests: no e2e for a 409 on save, a managed project (inputs disabled, no delete), or deleting a child project as a parent admin; no unit test for the role gating in `ProjectNav`.
- The role is fetched in the route and again in Settings (two `GET /projects/{path}` per admin tab load); admin tabs appear after the role loads (small layout shift).
