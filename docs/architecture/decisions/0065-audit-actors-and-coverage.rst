ADR-0065: Audit actors, coverage and project-scoped reads
=========================================================

:Status: Accepted
:Date: 2026-11-08

Issue :issue:`206`. Amends :doc:`0053-audit-events` on actors, coverage and read
access. The earlier text stays as written; where it differs, this ADR wins.

Context
-------

ADR-0053 listed M8 work as open: user and service-account actors, controller
duties as actors, audit of secret reads at dispatch, and per-subtree visibility.
M8 now has logins, service accounts and a project tree, so these are due.

Decision
--------

**Actors.** The grammar stays ``<kind>:<id>``:

- ``user:<sub>``: an OIDC login.
- ``sa:<service account id>``.
- ``admin-token:<first 12 hex of the hash>``.
- ``cli:<os user>``.
- ``system:<duty>``: ``scheduler``, ``retention``, ``dispatch``, ``dev-nodes``.
- ``webhook:<webhook id>``: runs started by a webhook.
- ``anonymous``: job and run calls without credentials while login is off and
  ``require_token`` is off.

**actor_name.** New column ``audit_events.actor_name`` (migration 00020): a
display snapshot of the actor when the event is written, for example a user's
email at that time. The store fills it from the request context only when the
event's actor is the context's actor. Names are never refreshed.

**New actions.**

- ``job.create`` and ``job.version.create`` (target type ``job``;
  ``project_path`` is the job's project; details ``name``, ``version``,
  ``checksum``). Written in the job's own transaction when the caller has an
  actor; imports and tests without an actor write none.
- ``run.trigger`` (target type ``run``; details ``job_id``, ``job_version_id``,
  ``trigger``, and ``schedule_id`` or ``webhook_id`` when they apply; never the
  options). Written in the transaction that creates the run, for API, webhook and
  schedule triggers. This replaces the ADR-0058 and ADR-0059 statements that
  fires are not audited: every scheduler fire now writes one ``run.trigger`` with
  actor ``system:scheduler``.
- ``run.cancel`` (target type ``run``; details ``job_id``).
- ``auth.login`` (target type ``user``, target the ``sub``; details
  ``instance_admin``; actor ``user:<sub>`` with ``actor_name`` the email).
  Written in the session's transaction.
- ``secret.read`` (target type ``execution``, target the execution id;
  ``project_path`` is the run's project; details ``attempt_id``, ``run_id`` and
  ``refs``, the sorted ``secret://scope/name`` refs; never values, never field
  names).

**secret.read semantics.** One event per dispatched attempt that carries
secrets. The dispatcher (actor ``system:dispatch``) writes it before it sends
``step.execute``. If the event cannot be written, the execution is requeued and
the secrets are not sent. There is no dispatch transaction, so a send that
fails after the event leaves an event for a read that did not complete: the log
can over-report but never under-report. Retries of one execution write one
event per attempt.

**Visibility.** ``GET /api/v1/audit`` is no longer admin-token only.

- An instance admin (admin token or ``admin_rule`` user) sees all events.
- With ``?project=<path>``, an admin of that project sees events whose
  ``project_path`` is the project or a descendant. No role on the project gives
  ``404``; a lower role gives ``403``.
- Without ``project``, anyone who is not an instance admin gets ``403``.
- Events with an empty ``project_path`` (instance level) are visible only to
  instance admins.

The subtree test is ``project_path = p OR (project_path >= 'p/' AND
project_path < 'p0')``. On Postgres ``project_path`` is ``COLLATE "C"``
(migration 00020) so the range compares bytewise. New index
``audit_events_project`` on ``(project_path, at, id)``.

**Guard.** ``TestEveryOperationHasAnAuthPolicy`` now also fails for an operation
that declares security but has neither a ``routePolicies`` entry nor the
``requireAdmin`` middleware. Review found ``list-audit-events`` briefly open.

Consequences
------------

- The "Not audited" item "secret reads at dispatch" of ADR-0053 is done.
- Audit volume grows: one event per trigger and per secret-carrying attempt.
  ``audit.retention_days`` (ADR-0054) bounds it.
- ``actor_name`` can be stale by design; ``actor`` is the identity.


Secret events and open gaps
===========================

- The ``secret.*`` events (``secret.create``, ``secret.update``, ``secret.delete``,
  ``secret.kek_rotate``, ``secret.read``) carry secret names, field names and refs, some
  of them node credentials under ``_inventory``. They are left out of every audit
  listing for a caller who is not an instance admin, even for a project admin reading
  their own subtree.
- Not audited yet: node inventory writes (create, update, delete, forget host key,
  ``sync-nodes``, ``batch-nodes``) and logout. They are instance-admin operations; the
  audit of them is future work.
- ``job.create`` and ``job.version.create`` are written when the store call has an actor
  in its context. Every API path has one (``authorize`` sets it, ``anonymous`` included);
  a caller without an actor, such as an import or a test, writes no event.
- Each attempt of an execution writes its own ``secret.read``: an agent that rejects
  ``step.execute`` and is retried gets the values again, and so a new event.
