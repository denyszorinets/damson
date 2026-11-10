ADR-0069: Jobs as code: apply, export, managed objects
======================================================

:Status: Accepted
:Date: 2026-11-08
:Amends: :doc:`0062-project-tree` (projects gain ``managed_by``; managed projects are read-only), :doc:`0064-access-rules-and-authorization` (route policies ``apply`` and ``export``, per-change checks), :doc:`0065-audit-actors-and-coverage` (new actions)

Issue :issue:`218`. Refines ARCHITECTURE section 13. Related: ADR-0023,
:doc:`0053-audit-events`.

Context
-------

ARCHITECTURE section 13 promised ``damson apply`` and ``damson export`` and
left open how a source owns objects, what pruning does, how apply is
authorized and what the UI may change. Runs keep ``job_id`` and ``runs.job_id``
has no cascade, so a job that ever ran cannot be hard-deleted without losing
history.

Decision
--------

**Layout.** The folder path relative to ``-f`` is the project path.
``<path>/project.json`` holds ``apiVersion`` (``damson.dev/v1``), ``kind``
(``Project``), ``description``, ``access_rules`` (``[{role, rule}]``) and
``node_scope``. It is declarative: a missing field means empty. Unknown fields
are rejected and there is no ``path`` field. ``<path>/<name>.job.json`` is the
full job document; ``metadata.project`` must equal the folder and
``metadata.name`` the file stem. JSON only; YAML stays open under
ADR-0023. A folder with ``project.json`` creates or updates the project,
parents first. Parents are never created implicitly. A folder with jobs and no
``project.json`` needs an existing project. No files at the root.

**API.** ``POST /api/v1/apply`` takes ``{source, prune, dry_run, adopt,
files: [{path, content}]}`` and returns ``{source, dry_run, changes, summary}``.
``source`` matches ``files:<name>`` with ``^[a-z0-9][a-z0-9._-]{0,62}$``; ``ui``
and ``git:*`` are refused over the API. Server limits: body 16 MiB, at most
2000 files, 1 MiB per file, slash-separated paths without ``..``, a leading
``/`` or duplicates, directory segments following the project grammar, file
names up to 128 bytes. ``project.json`` allows at most 50 access rules of 1 to
2048 bytes each, validated per file with a location. All stateless parse issues
are reported in one ``422`` with locations ``body.files[i].content.<field>``.
``prune`` with an empty file list is refused (``422`` "empty bundle").

**Pipeline.** Parse, Compute (a plan) and Execute. Parse and Compute are pure
and live in ``internal/apply``. ``store.ApplyStore`` supplies ``ApplyState``
and runs ``ApplyPlan`` in one transaction in which every write is a
compare-and-swap: jobs on ``current_version``, ``managed_by`` and archived
state, projects on ``version`` and ``managed_by``. A lost CAS rolls everything
back and answers ``409`` (retry). Every conflict from a concurrent change
(a stale CAS, a project or parent deleted meanwhile, a UI create-job taking the
name) answers the same ``409``. Jobs are compared by ``engine.Checksum``
(an unchanged job gets no new version); projects by description, rules and
scope. Actions: ``create``, ``update``, ``unchanged``, ``archive``,
``restore``, ``adopt``, ``release``. A dry run is the same path without
Execute and is not audited.

**Schema (migration 00023).** ``projects.managed_by TEXT NOT NULL DEFAULT
'ui'``, ``jobs.archived_at`` (nullable) and the partial index
``jobs_managed_by_idx``. ``jobs.managed_by`` exists since 00003.

**Ownership.** Objects owned by ``ui`` need ``adopt: true`` (CLI ``--adopt``).
An object owned by another source is always an error: that source must prune
it first. With ``prune``, the source's live jobs missing from the bundle are
**archived** and its projects missing from the bundle are **released** to
``ui``, never deleted. An archived job is hidden from lists, cannot run, keeps
its runs and still blocks project deletion; a later apply of the same name
restores it. Prune refuses (``422`` naming the job) when a job about to be
archived still has enabled schedules or unrevoked webhooks. Archive is also a
CAS that checks, inside the transaction, that no enabled schedule or unrevoked
webhook exists: one created since the plan makes the apply lose (``409``,
retry) instead of archiving a live job. A schedule fire or webhook on an
archived job records "the job is archived" as the fire error, so a schedule does
not stay due forever. Creating a job with the name of an archived job is
``409`` ("archived"): the name stays reserved until the owning source applies it
again. Another source cannot take an archived job; whether it may is left open. A source covers
everything it owns across projects. Without prune a removed file leaves the job
managed.

**Managed guards.** Creating a job version on a managed or archived job is
``409`` ("managed by X" or "archived"). ``PATCH`` project, ``PUT`` access rules,
``PUT`` node scope and ``DELETE`` project on a managed project are ``409``. The
guards are in SQL (``... AND managed_by = 'ui'``); the row is then re-read for
the message (``store.ManagedError`` matches ``ErrConflict``). Job access rules
(``job_access``) stay API-managed. Trigger-run, schedule create and webhook
create or fire on an archived job are ``409``. Accepted race: a trigger
concurrent with an archive can still start a run.

**Authorization.** Route policies ``apply`` (``editor``, handler-checked,
strict) and ``export`` (``admin``, handler-checked, strict). The handler checks
each change: job create, update, archive, restore and adopt need ``editor`` on
the job's project; project update, adopt and release need ``admin`` on it;
project create needs ``admin`` on the parent (instance admin for a root); an
unchanged project needs ``editor``. A service token (never admin) can therefore
apply job files and unchanged ``project.json``. Authorization comes before plan
problems: a problem about a project (missing parent, owned by another source,
ui-owned without adopt, prune blocked by bindings) is shown only to a caller
with at least ``editor`` on that project; otherwise the answer is ``404`` (no
role) or ``403`` (too low). Stateless parse problems are still reported first. Export needs
``admin`` on the project because it includes access rules.

**Audit.** Events are written in the same transaction and need an actor. They
reuse ``job.create``, ``job.version.create`` (details gain ``source``),
``project.create``, ``project.update``, ``project.access_rules.update`` and
``project.node_scope.update``, and add ``job.archive``, ``job.restore``,
``job.manage`` and ``project.manage`` (details ``{from, to}``). One
``request_id`` links the events of an apply. There is no summary event.

**Export.** ``GET /api/v1/export?project=P`` returns ``{files: [{path,
content}]}`` in the apply shape for the subtree: a ``project.json`` per project
and every live job (managed or not), sorted by path and deterministic. Export
then apply is a no-op (same checksums). Archived jobs are not exported. Job
names that do not survive as file names make export ``422``, listing them: a
leading dot (the CLI skips dot files, so ``apply --prune`` would archive the
job), control characters, path characters, over-long names, and names that
differ only by case within a project. Specs
hold only ``secret://`` refs.

**CLI.** ``damson apply -f DIR --source files:NAME [--prune] [--dry-run]
[--adopt] [--json]`` and ``damson export --project P -o DIR [--force]
[--json]``. ``--server`` or ``DAMSON_SERVER`` (default
``http://127.0.0.1:8080``); the token comes only from ``DAMSON_TOKEN`` or
``--token-file`` with no token flag so it never appears in ``ps``. Apply works
with an editor service token; export needs the admin token or an admin user. The
CLI resolves a symlinked ``-f`` root, skips dot entries, refuses symlinks inside
it and warns about other files. It refuses a folder holding no ``project.json``
or ``*.job.json`` file, so ``--prune`` never sends an empty bundle.
Objects exported from the UI need ``--adopt`` on the first apply; once the
source manages them, export then apply is a no-op. Exit 0 on success (also a dry run with changes), 1 on API or
validation errors (all issues printed), 2 on usage. Export writes each file
atomically, refuses a non-empty directory without ``--force``, re-validates
every path with the apply path rules and requires a project folder plus
``project.json`` or ``*.job.json`` (so ``.git/hooks``, scripts and root files
are refused). Writes go through an ``os.Root``, so a symlink in the target
folder cannot redirect a write.

**UI.** A "managed by <source>" badge on the job list, job detail and project
list; managed projects have delete disabled and read-only rules and scope
forms; an archived badge. An export button is a follow-up.

**Future Git sync.** A duty feeds the same Compute and ``ApplyPlan`` with
source ``git:<name>`` and actor ``system:git-sync``. The apply logic does not
change.

Alternatives Considered
-----------------------

- **Hard delete on prune.** Rejected: it would delete run history, and
  ``runs.job_id`` has no cascade.
- **Multipart or tar upload.** Rejected: JSON fits huma, OpenAPI and the
  generated client.
- **YAML now.** Rejected: JSON is the spec format today (ADR-0023).

Decision Criteria
-----------------

All-or-nothing apply, no loss of run history, no new infrastructure, and one
path for files today and Git later.

Rationale
---------

Soft delete keeps runs reachable and makes a pruned job restorable. Releasing
projects instead of deleting them avoids destroying secrets, children and
rules. Per-change authorization lets a project-scoped service token drive CI
without being admin.

Consequences
------------

Positive
~~~~~~~~

- Jobs live in a repository and are applied atomically and idempotently.
- Export gives a migration path from UI-made jobs.

Negative
~~~~~~~~

- Archived jobs remain in the database and block project deletion.
- Job access rules are not covered by the bundle.

Risks
~~~~~

- A lost CAS makes apply answer ``409``; CI must retry.
- A trigger concurrent with an archive can still start a run.

Follow-up
~~~~~~~~~

- Git sync duty, YAML, a UI export button.

Validation
----------

Unit tests for Parse and Compute, store tests on SQLite and Postgres, and API
and CLI tests (``internal/apply``, ``internal/api/apply_test.go``,
``internal/cli/apply_test.go``).

References
----------

- :doc:`/reference/jobs-as-code`
- ARCHITECTURE section 13
