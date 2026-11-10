---
title: "Project-first shell R10: audit follow-ups"
type: chore
status: open
source: pf-r10-keyboard-phone
---

## Items

- The drawer has no Escape handler, and when it closes on a link click focus drops to the body; return focus to the Menu button.
- The Menu button label replaced its visible text (WCAG 2.5.3 label in name); include the visible tab name in the accessible name.
- The "/" shortcut also fires when focus is on another button or select trigger outside a listbox or dialog.
- Tests: the in-list "/" step does not prove the guard ran (assert focus is inside the listbox first); `isTyping` and `arrowNav` have no unit test; the no-sideways check does not visit the run, job or secrets pages, and two raw tables (StepPanel, VersionHistory) skip the scrolling container.
- Home and End are taken from page scroll while a nav link has focus; note it in the docs.
