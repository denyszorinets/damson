---
title: "Project-first shell R8: audit follow-ups"
type: chore
status: open
source: pf-r8-admin-area
---

## Items

- Admin nav: `aria-current` is exact-match while the highlight uses a prefix, so a detail page (nodes/web-1) highlights Nodes without marking it current; use one `active` value. The admin branch has no `<main>` landmark.
- Log purge: the execution id stays editable while the confirmation shows; disable it while confirming.
- Tests: no e2e for a bearer-token operator (no session) seeing the Admin nav; the non-admin test covers /admin/audit, /admin/secrets and /admin only.
- The top-bar `admin` predicate duplicates `isInstanceAdmin()`; import the helper.
- Old redirects drop the query string and hash; forward them when filters become URL-driven.
- Agents were visible to every signed-in user and are now admin-only (the roadmap lists them under Admin); confirm that is wanted.
