ADR-0064: Access rules and authorization
========================================

:Status: Accepted
:Date: 2026-11-08

Issue :issue:`205`. Implements ARCHITECTURE section 12.4.

Context
-------

Login (ADR-0063) and service tokens (ADR-0060) identify a caller, but a user who
is not an instance admin could do nothing. Access must follow the project tree
(ADR-0062) and the identity provider's claims.

Decision
--------

**Roles.** ``viewer`` < ``runner`` < ``editor`` < ``admin``.

**Rules.** A rule is ``{role, rule}``. ``rule`` is a CEL expression over
``claims`` (the raw id token claims, ``dyn``) and ``user`` (``sub``, ``email``,
``name``, ``groups``). It must compile and be boolean (or have a type only known
at run time), is cost-limited, and a list holds at most 50 rules of at most 2048
bytes each. Compiled programs are cached by source text.

**Storage.** ``projects.access_rules`` (JSON text, default ``[]``) and the table
``job_access(job_id, rules, version, updated_at)``; no row means no rules and
version 0. Project rules may grant up to ``admin``. Job rules may grant up to
``editor`` and are additive for that job only.

**Writes.** Rules are validated and compiled on write: ``422`` with location
``body.rules[i].rule`` or ``body.rules[i].role``, never quoting the expression.
Project rules are a compare-and-swap on the project version (shared with
description updates). Job rules use their own version (``0`` creates). A stale
``if_version`` is ``409``.

**Effective role.** The maximum role of every matching rule on the job, the
project and all its ancestors. A rule that fails to evaluate (for example a
missing claim) or does not compile grants nothing; the others still apply, and
the log names the role and index only. Instance admins (the admin token, or a
user the OIDC ``admin_rule`` selected at login) are ``admin`` everywhere. A
service token holds its role where its project scope covers the target, and is
never admin (``NewServiceToken`` rejects ``admin``). A child rule does not climb
to the parent, and a job rule does not make the project visible in lists.

**404 or 403.** A caller with no role at all on the target gets ``404``,
indistinguishable from a missing one. A caller with some role that is too low
gets ``403``. An out-of-scope token gets ``404`` as before; a token with a role
too low gets ``403`` (the role is checked first).

**Routes.** ``routePolicies`` maps the routes governed by role: the 16
job, run and execution operations (``viewer`` for reads, ``runner`` for trigger
and cancel, ``editor`` for create job and create version), list-projects,
create-project, get, update and delete project, get and set project access
rules, and get and set job access rules. The project and access-rule routes are
strict: they never run anonymously, even with login off and ``require_token``
off. Create-project needs ``admin`` on the parent (instance admin for a root).
Get-project needs ``viewer``; update and delete need ``admin``; both access-rule
routes, reads included, need ``admin``. Create-job by a user needs ``editor`` in
``spec.metadata.project``; the handler checks it because the project is in the
body. List-projects returns only projects the caller holds a role on, each with
``my_role``. List-runs without ``job`` returns only runs of visible projects; it
reads up to 20 pages of 200 and returns a cursor, so a page can be short.

Still instance-admin only through ``requireAdmin``: secrets, nodes and inventory
sync, pools, service accounts, audit and log purge (secrets amended by
ADR-0077). Schedules and webhooks moved to
project roles in :doc:`0076-project-scoped-schedules-and-webhooks`. Agent,
pool and plugin metadata reads need any login. Scoping those per project is
future work.

**Grant cache.** Per controller, keyed ``session id|project|job``, 60 seconds
(injected clock), cleared when a rule is written on that controller. Other
controllers may serve a cached answer for up to a minute. Logout is not affected,
because the session is read from the database on every request.

**Audit.** ``project.access_rules.update`` and ``job.access_rules.update``
(target types ``project`` and ``job``; ``project_path`` is the project; details
``path`` or ``job_id``, ``count`` and ``roles``, never the expressions), written
in the same transaction as the change. The actor is ``user:<sub>``, ``sa:<id>``
or ``admin-token:<hash prefix>``.

**API.** ``GET`` and ``PUT /api/v1/projects/{path}/access-rules`` and
``GET`` and ``PUT /api/v1/jobs/{id}/access-rules``. The body is
``{rules: [{role, rule}], if_version}`` and the response ``{rules, version}``.
``Project`` gains ``my_role``.

Consequences
------------

- Deviation: the plan to default ``auth.require_token`` to true for the
  controller role is not done. Without OIDC the web UI has no login, so
  the job and run pages would stop working. Operators should configure OIDC
  (which ends anonymous access) or set ``auth.require_token=true`` themselves
  and use service tokens or the API. The startup warning for an open install
  stays.
- A changed rule on one controller can take up to 60 seconds to apply on
  another. Claims are frozen at login (ADR-0063), so a group removed at the IdP
  applies at the next login.
- Rules are code (CEL) written by project admins; they cannot reach secrets or
  anything outside ``claims`` and ``user``.


Further limits
==============

- Deleting a project needs the admin role on its **parent** (an instance admin for a
  root), like creating one, so a project admin cannot remove the node that holds their
  own authority. Cached roles are cleared when a project is created or deleted, so a
  recreated path does not inherit a role cached for the old one.
- A role cache entry computed while a rule was being changed is not stored (a
  generation counter), so a revoked role does not survive on the controller that wrote
  the change.
- Listings evaluate each project's own rules once and take the highest role along the
  path, so a listing costs one evaluation per rule, not per rule and descendant. A
  rule that fails to evaluate is logged at debug level.
- An open event stream (run events, execution logs) is checked when it connects. A
  later rule change, revoked token or logout does not end it until the client
  disconnects.
- ``GET /runs?job=<id>`` answers 404 for a job that does not exist, like for one the
  caller cannot see.

Addendum: explaining a role (2026-11-10)
----------------------------------------

The web UI shows where a role comes from (roadmap ``ui-overhaul``, R12). Two
policies the text above did not record:

- **Rule text is for instance admins.** ``POST /access/check`` returns the
  granting rule's expression to an instance admin. ``GET /access/me`` gives any
  signed-in user the kind of rule and, for an inherited role, the ancestor's path
  (a role on an ancestor means a role on that project too, so it is not hidden),
  never an expression: expressions can name other people and groups. A project
  admin can already read their own project's rules (``get-project-access``).
- **A check is audited though it changes nothing.** ``access.check`` records the
  number of groups, whether an email was given, the role and the source kind,
  never the groups, the email or a rule. The request fails if the event cannot be
  written.

``policy.Evaluator.Role`` is defined as ``Explain(...).Role``, so what is
explained is what is enforced. The checked identity holds only the groups and the
email: rules on other claims grant nothing in a check (decision D4: no raw claims
and no acting as another user).
