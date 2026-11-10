ADR-0076: Project-scoped authorization for schedules and webhooks
=================================================================

:Status: Accepted
:Date: 2026-11-10

Builds on ADR-0064 (access rules and authorization), ADR-0058 (schedules) and
ADR-0059 (webhooks). Moves the schedule and webhook routes from instance admin
to project roles.

Context
-------

Schedules and webhooks belong to a job, and so to a project, yet ADR-0064 left
them instance-admin only. A project admin could not manage the triggers of their
own jobs, and a service token had no access at all.

Decision
--------

**Roles.** All nine operations are strict: they never run anonymously, even
with login off and ``require_token`` off.

.. list-table::
   :header-rows: 1

   * - Operation
     - Role on the job's project
   * - list and get schedule
     - ``viewer``
   * - create, patch (pause and resume included) and delete schedule
     - ``editor``
   * - list and get webhook
     - ``viewer``
   * - create and revoke webhook
     - ``admin``

A ``runner`` cannot pause or resume a schedule. A webhook read never returns the
token or its hash. The token appears once, in the create response, and never in
audit events, logs or reads. Job access rules count (``roleOn(project, jobID)``);
they cap at ``editor``, so they never suffice for webhook create or revoke. An
instance admin may do everything.

**Target resolution** (``internal/api/target.go``).

- ``subjSchedule`` and ``subjWebhook`` (path ``{id}``): load the row and use the
  project of its job. A missing row is ``tgtMissing``; a gone parent is
  ``tgtNoParent``.
- ``subjTriggerList`` for the list routes: with ``job_id`` it resolves like a
  job; otherwise ``project`` must exist (else ``tgtMissing``); otherwise
  ``tgtAll``.
- Create schedule and create webhook use ``subjHandler``: the handler resolves
  the project of ``body.job_id`` and calls ``require(project, jobID, need)``.
  ``schedules.job_id`` and ``webhooks.job_id`` are immutable and jobs never
  change project, so nothing can change between the middleware check and the
  handler.
- ``tgtAll``: an instance admin may do everything; a token uses its scope as the
  project (scope ``""`` means all); a user gets 400 ``job_id or project
  required``.

**List filter.** Both lists gain an additive ``project`` query that means the
project and its subtree and needs ``viewer`` on it (children inherit).
``ListSchedules`` and ``ListWebhooks`` take ``Filter{JobID, ProjectPath}``,
joined in shared SQL with ``path = p OR`` a prefix match on ``p/`` (no
``LIKE``).

**Service tokens.** The role rank is checked first (403), then the scope (404).
``NewServiceToken`` rejects the ``admin`` role, so a token never creates or
revokes a webhook. Before this change a token had no access to either resource
(401); it is now role based. A non-admin session got 403 and now gets 404 or 403
by role. Anonymous stays 401.

**Escalation analysis.**

- An editor who creates a schedule gains nothing: they can already trigger the
  same run and edit the job spec.
- Fires stay actor ``system:scheduler`` through ``Starter.Prepare``. Options are
  re-validated at fire time, and ``secrets.Authorize`` limits secret refs to the
  job's project and its ancestors.
- No creator identity is stored or used. A schedule keeps firing after its
  creator loses the role, because it belongs to the project like the job.
- A webhook token gives trigger rights to whoever holds the URL, so creating and
  revoking one needs ``admin``.

**Audit.** ``schedule.create``, ``schedule.update``, ``schedule.delete``,
``webhook.create`` and ``webhook.revoke``. The store fills ``ProjectPath`` in
the write transaction from the job and its project. Actors are ``user:``,
``sa:`` and ``admin-token:``. Options, the token and its hash never appear in
details. Project admins read these events through ``GET /audit?project=``.

**Compatibility.** Same paths, operation ids and shapes; ``project`` is
additive. ``requireAdmin`` and ``adminSecurity`` are removed from these
operations and the routes move into ``routePolicies``. Instance admin and the
admin token behave as before. The UI ``JobTriggers`` keeps working; project
tabs come with R13.

Alternatives Considered
-----------------------

- Keep them instance-admin only: rejected, project admins could not run their
  own triggers and tokens could not be scoped.
- Let ``editor`` create webhooks: rejected, the URL is a bearer secret that
  grants trigger rights to its holder.
- Store the creator and fire as that identity: rejected, a schedule belongs to
  the project and must survive a change of roles.

Consequences
------------

- Viewers already see ``secret://`` refs in specs and in schedule and webhook
  options. The R14 item "names hidden from viewer and runner" concerns only the
  secrets listing.
- A schedule outlives its creator's access; removing it needs ``editor``.
- Tokens can never create or revoke webhooks.

Tests
-----

Run on SQLite and Postgres:

- Users: viewer, runner, editor, admin, job-rule editor, no role (404), instance
  admin and admin token. Tokens: viewer, editor, out of scope (404) and
  unscoped. Every one of the nine operations.
- Lists filtered by project subtree and by ``job_id``; a user on ``tgtAll`` gets
  400; anonymous gets 401.
- The token is absent from get, list and audit; audit ``project_path`` is set.
- A route coverage test; a secret ref outside the chain is denied.

References
----------

- :doc:`0058-schedules-and-the-scheduler-duty`
- :doc:`0059-webhooks`
- :doc:`0064-access-rules-and-authorization`
- ARCHITECTURE sections 12.3 and 12.4
