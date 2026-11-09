ADR-0072: Forward-only migrations and an explicit migrate command
=================================================================

:Status: Accepted
:Date: 2026-11-09
:Amends: ARCHITECTURE section 16 (migrations)

Issue :issue:`232`. Related: ADR-0071 (the same release train), and the
expand/contract rule in :doc:`/operations/upgrade`.

Context
-------

Migrations ran only inside the controller (``--auto-migrate``). Operators who
want a one-off migration step (a Kubernetes hook Job, an ``ExecStartPre=``, a CI
check) had to start a controller for it. The migration files also carry goose
``Down`` sections that nothing runs and nothing tests, which invites the
assumption that a rollback is available.

Decision
--------

**Command.** ``damson migrate up`` and ``damson migrate status``, with
``--config``, ``--json`` and (status only) ``--check``. Database settings come
from the controller configuration and environment, as for ``damson admin``. The
controller role configuration is loaded but serving validation is not run, so a
migration Job needs only the ``DAMSON_DATABASE__*`` settings.

**``up``.** Refuses with exit status 1 when the database version is newer than
the binary. Otherwise it runs the same migrate function as ``--auto-migrate``
(goose migrations under a PostgreSQL advisory lock, then the job queue's own
migrations) and prints ``migrated from version A to B``. It is idempotent.

**``status``.** Prints the database version and whether it is up to date, has
pending migrations or is newer than the binary. It never changes the database.
``--check`` exits 1 for pending or newer, for CI and hooks. Usage errors exit 2.

**Forward only.** There is no ``down``. The ``Down`` sections in the migration
files are unsupported and nothing runs them. Rollback is a restore of the
backup taken before the upgrade, then the old binary. An old binary must not
run against a newer schema; ``status`` reports that case.

**Rolling upgrades.** Unchanged: migrations must be tolerated by the previous
version (expand/contract). ``--auto-migrate`` stays and is still the default
in the chart and the systemd unit.

Why no down
-----------

- A down path is code that runs rarely and under pressure. Without a test for
  every migration it is likely broken when needed, and we do not test them.
- Many down steps lose data (a dropped column cannot be refilled), so a
  "rollback" would silently discard what the new version wrote.
- Backups are the real rollback and also cover failures a down script cannot,
  such as a half-applied migration. Operators need them anyway.

Alternatives
------------

- **Support goose down.** Rejected for the reasons above.
- **Down tests in every release.** Each migration would need a test that
  applies it, writes data and reverses it. The cost is high, data loss stays,
  and it would still not replace the restore.
- **Only ``--auto-migrate``.** Rejected: it gives no way to migrate before the
  roll, to preview what is pending, or to gate a pipeline.

Consequences
------------

- Operators get a preview (``status``) and a one-off step (``up``) for hooks.
- A release that needs an incompatible schema change still requires stopping
  the controllers, because nothing can undo it online.
- The ``Down`` sections stay in the files; nothing runs them and they are unsupported.
