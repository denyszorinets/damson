---
title: "Project-first shell R9: audit follow-ups"
type: chore
status: open
source: pf-r9-home-search
---

## Items

- A search error stays above the project table after the query is cleared; show it only while a query is typed.
- The searchable set is fetched once per page mount and not refreshed by the Home poll, so a project granted meanwhile is not found until reload.
- A user-typed regex runs unguarded on every keystroke (catastrophic backtracking only hurts the typist); cap the pattern length if it matters.
- Valid-looking flag mixes the engine rejects (`/x/uv`) show the raw error text.
