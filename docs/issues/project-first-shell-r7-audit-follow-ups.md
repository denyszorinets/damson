---
title: "Project-first shell R7: audit follow-ups"
type: chore
status: open
source: pf-r7-project-secrets
---

## Items

- Token mode (no login): the Secrets, Access and Settings tabs read as not found because there is no role; decide whether a bearer token operator should see them.
- New version of a generic secret: fields left blank are omitted from the request; confirm the server replaces the whole bundle and warn in the UI, or require every field.
- The multiline field is an unmasked textarea (acceptable for PEM keys; add a note).
