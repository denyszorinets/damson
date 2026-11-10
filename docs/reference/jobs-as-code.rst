.. _reference-jobs-as-code:

Jobs as code
============

``damson apply`` makes a folder of JSON files the source of truth for projects
and jobs; ``damson export`` writes such a folder from a running server. Design:
:doc:`/architecture/decisions/0069-jobs-as-code`.

Layout
------

The folder path relative to ``-f`` is the project path.

.. code-block:: text

   jobs/
     infra/
       project.json
       billing/
         project.json
         deploy-report-tool.job.json

``project.json``:

.. code-block:: json

   {
     "apiVersion": "damson.dev/v1",
     "kind": "Project",
     "description": "Billing infrastructure",
     "access_rules": [{"role": "runner", "rule": "'billing-ops' in claims.groups"}],
     "node_scope": "'billing' in node.tags"
   }

It is declarative: a missing field means empty (no rules, no scope). Unknown
fields are rejected and there is no ``path`` field. ``<name>.job.json`` is a
full :doc:`job document <job-spec>`; ``metadata.project`` must equal the folder
and ``metadata.name`` the file name without ``.job.json``.

.. code-block:: json

   {
     "apiVersion": "damson.dev/v1",
     "kind": "Job",
     "metadata": {"name": "deploy-report-tool", "project": "infra/billing"},
     "spec": {"steps": []}
   }

Only JSON is supported (YAML stays reserved, ADR-0023).

* A folder with ``project.json`` creates or updates that project, parents
  first. Parents are never created implicitly.
* A folder with jobs but no ``project.json`` needs an existing project.
* No files at the root.

``project.json`` allows at most 50 access rules of 1 to 2048 bytes each; a
violation is reported per file with a location.

Limits (server): body 16 MiB, at most 2000 files, 1 MiB per file. Paths are
slash-separated, without ``..``, a leading ``/`` or duplicates; directory
segments follow the project grammar and a file name is at most 128 bytes. The
CLI resolves a symlinked ``-f`` root, skips dot entries, refuses symlinks
inside it and warns about other files. It refuses a folder with no
``project.json`` or ``*.job.json`` file, and the server refuses ``--prune`` with
an empty file list (``422`` "empty bundle").

Workflow
--------

.. code-block:: shell

   export DAMSON_TOKEN=...            # or --token-file
   damson export --project infra -o jobs      # start from what exists
   # edit the files
   damson apply -f jobs --source files:main --dry-run --adopt
   damson apply -f jobs --source files:main --adopt

A dry run computes the same plan without writing or auditing. The output lists
``+`` create, ``~`` update/adopt/restore/release, ``-`` archive and ``=``
unchanged lines, then a summary; ``--json`` prints the API answer.

Adopting existing objects
~~~~~~~~~~~~~~~~~~~~~~~~~

Objects made in the UI are owned by ``ui``. Applying over them is refused
until you pass ``--adopt``; they then become ``files:<name>``. Objects
exported from the UI therefore need ``--adopt`` on the first apply; once the
source manages them, export then apply is a no-op. An object
owned by another source is always an error: that source must prune it first.

Prune
~~~~~

With ``--prune`` the source's live jobs missing from the bundle are
**archived**: hidden from lists, they cannot run, their runs keep their
``job_id``, and they still block deleting the project. Applying the same job
name again restores it. The source's projects missing from the bundle are
**released** to ``ui``, never deleted. Prune is refused (``422`` naming the
job) when a job to archive still has enabled schedules or unrevoked webhooks. Archive is
also checked inside the transaction: a schedule or webhook created since the
plan makes the apply lose (``409``, run it again) instead of archiving a live
job. A schedule fire or webhook on an archived job records "the job is
archived" as the fire error. Creating a job with the name of an archived job is
``409`` ("archived"); the name stays reserved until the owning source applies it
again (whether another source may take it is left open). A source covers everything it owns, across projects. Without ``--prune`` a
removed file leaves the job managed.

Managed objects
~~~~~~~~~~~~~~~

* Creating a version of a managed or archived job is ``409``
  (``managed by files:main`` or ``archived``).
* Updating, changing rules or scope of, or deleting a managed project is
  ``409``. Job access rules stay managed through the API.
* Triggering a run, creating a schedule, and creating or firing a webhook on
  an archived job is ``409``. A trigger concurrent with an archive can still
  start a run.
* The UI shows a "managed by" badge on jobs and projects and an archived
  badge; managed projects cannot be deleted and their rules and scope are
  read-only.

CI example
----------

Use a service token scoped to the project with role ``editor``
(:doc:`service-accounts`). It can apply job files and unchanged
``project.json``; project changes need ``admin`` (admin token or user). Export
needs ``admin`` on the project, so a service token cannot export; use the admin
token or an admin user.

.. code-block:: yaml

   steps:
     - run: damson apply -f jobs --source files:main --prune
       env:
         DAMSON_SERVER: https://damson.example.com
         DAMSON_TOKEN: ${{ secrets.DAMSON_TOKEN }}

A ``409`` means a concurrent change won (a stale version, a project deleted
meanwhile, a UI job taking the name); run it again.

Authorization and audit
-----------------------

``POST /apply`` needs ``editor`` and ``GET /export`` needs ``admin``; both are
strict and checked per change. Job create, update, archive, restore and adopt
need ``editor`` on the job's project. Project update, adopt and release need
``admin`` on it; project create needs ``admin`` on the parent (instance admin
for a root); an unchanged project needs ``editor``. Authorization
comes before plan problems: a problem about a project (missing parent, owned by
another source, ui-owned without adopt, prune blocked by bindings) is shown only
to a caller with at least ``editor`` on it; otherwise the answer is ``404`` (no
role) or ``403``. Parse problems are still reported first. Events are
listed in :doc:`audit`; the events of one apply share a ``request_id``.

Export
------

``damson export --project P -o DIR`` writes ``project.json`` for every project
in the subtree and a ``.job.json`` for every live job, managed or not, sorted
and deterministic. Applying an export again changes nothing. Archived jobs are
not exported; job names that do not survive as file names are ``422`` and are listed: a
leading dot (``apply --prune`` would archive it), control or path characters,
over-long names, and names differing only by case within a project. Export
writes inside an ``os.Root`` and only paths that pass the apply rules and sit in
a project folder as ``project.json`` or ``*.job.json``. Specs hold only
``secret://`` references.
