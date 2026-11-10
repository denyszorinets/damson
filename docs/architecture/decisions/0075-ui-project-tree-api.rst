ADR-0075: UI project tree API
=============================

:Status: Proposed
:Date: 2026-11-10

Roadmap ``docs/roadmap/ui-overhaul.md`` (R3). Builds on ADR-0062 (project tree)
and ADR-0064 (access rules). Settles the response shape and the cost of the
queries behind the sidebar tree (R4) and its roll-ups (R9).

Context
-------

The UI needs one view of the projects and jobs a user may use, nested by path.
What a user may see is decided by CEL access rules, which only Go can evaluate,
so the server cannot prune in SQL. Nothing may hint at what is hidden: not an
ancestor, a count, a "has children" flag or an error.

Decision
--------

**The visible set is downward closed.** ``projectRoles`` gives a role on a
project from its own rules or from an ancestor's. So if a project is visible, all
its descendants are visible. A roll-up over a visible node's subtree therefore
never includes a hidden project. A visible node whose parent is not visible is a
root of the user's tree; no stub is made for the parent.

**One helper.** A single ``visibleScope(principal)`` in ``internal/api`` returns
the project roles (today's ``projectRoles``) and the jobs reached only through
job rules. R4, R5, R8, R9 and R12 call it and nothing else decides visibility.
It is cached per session under the existing grant cache (60 s, cleared by rule
edits).

**Tree response.** ``GET /api/v1/tree`` returns one level, keyset-paged by path (``cursor`` is the
last path returned, ``limit`` is clamped like other lists):

- ``parent`` (query, optional): a visible project path; absent means the user's
  roots. A parent that is not visible or does not exist gives the same 404.
- A node: ``path``, ``label`` (the full path for a root, the last segment
  below it), ``my_role``, ``has_children`` (over visible children), ``jobs``
  (jobs in the subtree), ``failed_24h`` and ``running`` (runs in the subtree).
  R4 returns the count fields as zero-valued optional fields and R9 fills them,
  so the generated client changes once.
- Jobs of a project come from the existing per-project jobs listing, with
  ``my_role``, last run status and time added.

**Shared with me.** ``GET /api/v1/tree/shared`` lists jobs whose project is not
visible but a job rule grants a role. Items carry the job name and role only,
never the project path or its siblings, and are not counted in any tree node.

**Roll-ups.** One grouped query per request, not per node. ``:since`` goes
through ``store.NormalizeTime`` (SQLite stores fixed-width text, so the compare
must be on the same format). ``runs_finished_idx`` serves the failed rows and
``runs_status_idx`` the running ones. The ``jobs`` count is a second grouped
query, ``SELECT project_id, count(*) FROM jobs GROUP BY project_id``::

   SELECT j.project_id, r.status, count(*) FROM runs r JOIN jobs j ON j.id = r.job_id
   WHERE (r.status = 'failed' AND r.finished_at >= :since) OR r.status = 'running'
   GROUP BY j.project_id, r.status

It is portable (``queries/shared``). Go adds each group to the project and to
every ancestor that is in the visible set, so nothing counts twice.

**Job-rule lookup.** ``SELECT ja.job_id, ja.rules, j.project_id FROM job_access ja
JOIN jobs j ON j.id = ja.job_id WHERE ja.rules <> '[]' ORDER BY ja.job_id
LIMIT :max`` reads at most ``maxJobRuleScan`` rows (1000, the size measured
below; empty lists are skipped, since an edited-away rule leaves ``[]``); the rules are compiled and evaluated in Go for the caller,
skipping jobs whose project is visible. The result is cached with the grant
cache. A user over the bound sees ``truncated: true`` rather than a silent cut.

**One answer for "no role" and "does not exist"**: ``require`` already gives
``ErrNotFound`` for no role; the tree endpoints and the job and project pages
use it too, so the body is the same 404. The UI renders "not found, or you may
not have access".

Measured
--------

Throwaway code (removed) on SQLite and Postgres 18, 200 projects, 2000 jobs, 10
000 runs (a fifth failed or running, spread over 48 hours) and 1000 job rule rows.
Three runs each:

=================================== ======== ==========
Step                                SQLite   Postgres
=================================== ======== ==========
Grouped failures and running        ~5.7 ms  ~3-4 ms
Job rule rows read                  ~1 ms    ~0.9 ms
Compile and evaluate 1000 rules     ~50 ms   ~48 ms
=================================== ======== ==========

Both stay under the 100 ms target. The cost is rule compilation, so the cache is
required. The Postgres run was a local server, not a container (no Docker on
the machine): re-measure under ``make test-pg`` if the shape of the data
changes.

Alternatives Considered
-----------------------

- Ancestor stubs so a nested role has a parent node: rejected, it reveals that
  the parent exists.
- Counting job-rule-only jobs in the tree: rejected, it needs a project node
  that the user may not see.
- Per-node recursive counts: rejected, one query per node.

Consequences
------------

- Downward closure holds only while ``ListProjects`` is complete and ordered
  parents first, as ``projectRoles`` assumes.
- Totals can differ between users; the user documentation says so.
- Compiled rules are not stored; if the job-rule count grows past the bound,
  cache compiled programs per rule version.

References
----------

- :doc:`0062-project-tree`
- :doc:`0064-access-rules-and-authorization`
- Roadmap ``docs/roadmap/ui-overhaul.md``, slice R3
