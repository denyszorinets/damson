.. _reference-access-rules:

Access rules
============

Access rules give users roles on projects and jobs from their identity
provider claims. Design: :doc:`/architecture/decisions/0064-access-rules-and-authorization`.

Roles
-----

``viewer`` < ``runner`` < ``editor`` < ``admin``. A higher role includes the
lower ones.

.. list-table::
   :header-rows: 1

   * - Operation
     - Role needed
   * - Read jobs, versions, graphs, runs, executions, logs; validate a spec
     - ``viewer``
   * - Trigger a run, cancel a run
     - ``runner``
   * - Create a job (in ``spec.metadata.project``), create a job version
     - ``editor``
   * - List projects (only those you hold a role on), get a project
     - ``viewer``
   * - Create a project
     - ``admin`` on the parent (instance admin for a root project)
   * - Update or delete a project
     - ``admin``
   * - Read or set project or job access rules
     - ``admin``

Secrets, nodes and inventory sync, pools, service accounts, schedules,
webhooks, audit and log purge are for instance admins only. Any login may read
agent, pool and plugin metadata. Scoping those per project is future work.

Rules
-----

A rule is ``{"role": ..., "rule": ...}``. ``rule`` is a CEL expression over
``claims`` (the raw id token claims) and ``user`` (``sub``, ``email``,
``name``, ``groups``). It must compile and be boolean. A list holds at most 50
rules of at most 2048 bytes. Examples:

.. code-block:: text

   'billing-ops' in claims.groups
   user.email.endsWith('@example.org')
   claims.hd == 'example.org'

Rules attach to a project, where they apply to it and every descendant (up to
``admin``), or to one job, where they are additive for that job only (up to
``editor``). The effective role is the highest role of every matching rule on
the job, the project and all its ancestors. A child rule does not give access
to the parent, and a job rule does not make its project appear in lists.

A rule that fails to evaluate (for example the claim is missing) or does not
compile grants nothing; the other rules still apply. The log names the role and
index, never the expression.

Instance admins (the admin token, or a user selected by
``auth.oidc.admin_rule``) are ``admin`` everywhere. Service tokens hold their
role where their project scope covers the target and are never admin
(:doc:`service-accounts`).

404 or 403
----------

A caller with no role at all on a target gets ``404``, the same as for a
missing one. A caller with a role that is too low gets ``403``. A token whose
scope does not cover the target gets ``404``; a token with a role too low gets
``403`` (the role is checked first).

The project and access-rule routes never run anonymously, even with login off
and ``auth.require_token`` off.

Setting rules
-------------

.. code-block:: shell

   curl -sX PUT http://localhost:8080/api/v1/projects/infra%2Fbilling/access-rules \
       -H "Authorization: Bearer $DAMSON_TOKEN" \
       -H 'Content-Type: application/json' \
       -d '{"if_version": 1, "rules": [
             {"role": "runner", "rule": "\"billing-ops\" in claims.groups"},
             {"role": "viewer", "rule": "claims.hd == \"example.org\""}]}'
   curl -sX PUT http://localhost:8080/api/v1/jobs/$JOB_ID/access-rules \
       -H "Authorization: Bearer $DAMSON_TOKEN" \
       -H 'Content-Type: application/json' \
       -d '{"if_version": 0, "rules": [
             {"role": "editor", "rule": "user.email == \"ana@example.org\""}]}'
   curl -s http://localhost:8080/api/v1/projects/infra%2Fbilling/access-rules \
       -H "Authorization: Bearer $DAMSON_TOKEN"

The response is ``{"rules": [...], "version": N}``. Project rules use the
project version (shared with description updates); job rules have their own
version, and ``0`` creates them. A stale ``if_version`` gives ``409``. A rule
that does not compile or is not boolean, or a bad role, gives ``422`` at
``body.rules[i].rule`` or ``body.rules[i].role``, without the expression. A job
rule cannot grant ``admin``.

Audit
-----

``project.access_rules.update`` and ``job.access_rules.update`` (target types
``project`` and ``job``) record the project as ``project_path`` and details
``path`` or ``job_id``, ``count`` and ``roles``, never the expressions. The
actor is ``user:<sub>``, ``sa:<id>`` or ``admin-token:<hash prefix>``.

Caching
-------

Each controller caches a grant per session, project and job for 60 seconds and
clears its cache when a rule is written on it. Another controller may serve a
cached answer for up to a minute after a change. Logout takes effect at once,
because the session is read from the database on every request. Claims are
frozen at login, so a group removed at the identity provider applies at the
next login.
