---
title: UI overhaul run: final review findings
type: task
status: open
source: ui-overhaul run (final review)
---

## Context

Should and Nice findings of the final review of the `ui-overhaul` run (spec and standards axes), not fixed in the run.

## Problem

Spec:
- R13 (audit page) and R12 (effective-access view: built, audited twice, unshipped because the audit cap was spent) are not in the repo; G6 and G7 stay unmet.
- The access check cannot show that a group would be an instance admin (the admin rule is evaluated at login): no "claim" source kind.
- A check with both `job_id` and `project` ignores `project` silently.
- "New job" disappears for a viewer instead of showing disabled with the missing role (G3).
- The Access link shows only to signed-in users (kind `user`); an admin-token operator cannot reach the check.
- R6: a reload does not resume from the last sequence (stored `lastSeq` per execution in sessionStorage would do it). R14: browser-local saved selectors; document that.
- G8 (phone width, keyboard) has no scheduled slice.

Standards:
- Raw `<select>`, `<textarea>` and `<button>` in several new components where `ui/select`, `ui/textarea` and `ui/button` exist (VersionHistory, nodes page, SpecEditor, LogViewer, ProjectTree, TreeNode, StepPanel).
- The two `ProjectCounts` bodies (sqlite, postgres) are identical apart from the timestamp argument; share the aggregation in `store/stats.go`.
- `secrets/+page.svelte` (370 lines) and `nodes/+page.svelte` are near the ~400 line convention; `internal/api/jobs.go` is 402.

R12 audit round (after the cap was raised), not fixed in the run:
- `accesscheck.go` `sharedFor` reads at most 1000 job rules and drops the rest silently; with more, `/access/me` omits job-only roles and the page says "You hold no roles". Read limit+1 and report truncation, as `tree.go` does.
- `checkAccess` is about 95 lines; split input normalising and target resolving.
- The access-check tests pushed `internal/api/tree_test.go` past 400 lines; move them to `accesscheck_test.go`.
- `identityFor` sorts the groups slice after the claim list is built, so the two orders can differ.
- The check's English `note` is rendered verbatim by the UI.

R13 audit round, not fixed in the run:
- The audit page's Load more and system toggle read the live filter fields, not the last applied filter; keep an applied copy so page 2 matches page 1.
- `hide_system` uses `actor NOT LIKE 'system:%'`, case-insensitive on SQLite and case-sensitive on Postgres; compare on a lowercased or prefix-range form so both agree.
- `storetest/secrets.go` is over 500 lines; move the audit tests to `storetest/audit.go`.
- The action and actor choices come only from loaded pages, ids only, and never include system actors while they are hidden; offer names and a distinct-values source.
- `from` and `to` are hand-parsed strings; a `format:"date-time"` tag would put the format in the OpenAPI schema.
- No e2e of Load more combined with a filter.

Final review of R12 and R13, not fixed in the run:
- `utcTime` drops an unparsable `from` or `to`, so a half-typed time filter silently shows an unfiltered list.
- The audit actor filter matches the id only; filtering by the shown name or email returns nothing.
- `from` later than `to` returns an empty page; a 400 would say why.
- `/access/check` on a missing project returns a bare 404 with no message; `p, _ := PrincipalFrom(ctx)` in `/access/me` drops the error.
- No test pins that a check result equals the enforced role for the same identity.
- The fail-closed audit of `access.check` (an audit failure returns no result) is not in the ADR.
(A reviewer's claim that `jobRules[g.Index]` can go out of range was rejected: `Explain` and the rule lookup use the same in-memory lists from one read.)

## Acceptance

- Each point fixed or dropped with a reason.
