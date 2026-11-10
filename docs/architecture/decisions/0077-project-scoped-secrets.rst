ADR-0077: Project-scoped authorization for secrets
==================================================

:Status: Accepted
:Date: 2026-11-10

Builds on ADR-0064 (access rules and authorization), ADR-0015 (secret
references) and ADR-0076 (schedules and webhooks). Moves the secrets routes
from instance admin to project roles.

Context
-------

Secrets belong to a scope, which is a project path, yet ADR-0064 left their
routes instance-admin only. A project admin could not manage the secrets their
own jobs use.

Decision
--------

**Scope and role.** A secret scope is the exact path of an existing project, or
``_inventory``. A caller's role on scope S is ``roleOn(S, "")``: the rules of S
and its ancestors, so roles inherit downward and an ``acme`` admin manages the
secrets of ``acme`` and every descendant. Job rules never count.
``secrets.Authorize`` (a job uses only the secrets of its own project and its
ancestors), the dispatch path and masking are unchanged. Values are never
returned.

**Routes.** All three are strict and never run anonymously.

- ``list-secrets``: ``editor``.
- ``put-secret`` and ``delete-secret``: ``admin``.

``requireAdmin`` is removed (``adminSecurity`` stays). The routes use
``subjHandler``, not a path subject: ``ctx.Param("scope")`` can still be
percent-encoded (``acme%2Fpayments``), which fails ``ValidateProjectPath`` and
would resolve as ``tgtMissing`` and pass the middleware; the handler then
decodes the scope and a put would create it, an authorization bypass. So the
handler authorizes the decoded scope with ``requireScope(ctx, scope, need)``:

- An instance admin is always allowed (any valid scope, orphans, ``_inventory``).
- Anyone else gets 404 for ``_inventory``, an invalid path or a project that
  does not exist; otherwise ``require(scope, "", need)``.
- In put, the first line clears the field values (deferred), so a denied path
  never keeps them; then ``requireScope``, then ``secretsReady``.

**List.** ``scope=S`` lists exactly S and needs ``editor`` on S. The new
additive ``project=P`` lists P and its ancestors, the scopes P's jobs may use,
and needs ``editor`` on P. It pages over at most 8 exact-scope ``List`` calls
of the backend in scope order, keyset ``(scope, name)``, same cursor format; no
new store method, so the db and OpenBao backends both work. Giving both is 400.
Giving neither: an instance admin lists everything (compatibility); everyone
else, an unscoped token included, gets 400 ``scope or project is required``,
which also keeps ``_inventory`` and orphan scopes out. The UI labels the origin
as ``scope != P``.

**Service tokens.** ``NewServiceToken`` rejects ``admin``, so a token never
writes or deletes (403 in scope, 404 out of scope). An editor token lists within
its scope, and ``project=P`` with P covered also returns ancestor names, like a
user. Before this change a token got 401; it is now role based.

**Names.** An editor seeing names is acceptable: an editor on P can already
reference any secret in P's ancestor chain in a spec and dispatch delivers it,
so names reveal strictly less. Viewer and runner get no listing but still see
``secret://`` refs in specs and options (ADR-0076). Known asymmetries: a child
editor sees ``acme`` names through ``project=child`` but gets 404 on
``scope=acme`` and on a put to ``acme``; a job-rule editor gets no listing.

**Masking.** No new code path touches a secret bundle.

**Audit.** Unchanged: ``project_path`` is already the scope. ``_inventory`` rows
carry ``project_path=_inventory``, so only an instance admin sees them. Project
admins read their events through ``GET /audit?project=``. Details never hold
values.

**Non-project scopes.** ``_inventory``, orphans left by delete-project when only
DB secrets are counted, and a future site-wide scope stay instance-admin only. A
site-wide scope is outside R14 (see
``docs/issues/project-level-and-site-wide-secrets-nodes-schedules-and-webh.md``).

**Compatibility.** Same operation ids, paths and shapes; ``project`` is
additive. The admin token, the CLI and the admin UI are unchanged. A non-admin
session that got 403 now gets 404 or 403 by role. Anonymous stays 401.

Alternatives Considered
-----------------------

- Keep secrets instance-admin only: rejected, project admins could not manage
  the secrets their jobs use.
- Authorize by the path subject: rejected, an encoded scope would bypass the
  middleware (see Routes).
- A new store method to list by ancestor chain: rejected, it would not serve the
  OpenBao backend.

Consequences
------------

- A project admin manages secrets of their subtree, never of a parent.
- Tokens can never write or delete secrets.
- Listing asymmetries above are deliberate and documented.

Tests
-----

Run on SQLite and Postgres:

- Admin of ``acme/payments`` puts, lists and deletes there (201, 200, 204) and
  gets 404 on a sibling, on ``acme``, on ``_inventory`` and on a missing project;
  an instance admin gets 201. Admin of ``acme`` manages ``acme/payments``.
- Editor: list 200, put 403. Viewer and runner: list 403. No role: 404. Job-rule
  editor: 404.
- ``project=acme/payments`` returns its own names plus ``acme``, never
  ``_inventory``, and pages across scopes. Both queries: 400. Neither, as a user
  or an unscoped token: 400.
- An encoded ``acme%2Fpayments`` by a non-member: 404 and nothing created.
- Tokens: viewer and editor, in and out of scope.
- A canary value is absent from the put response, the audit rows (``GET /audit``)
  and captured log output, also for denied puts (404 and 403). The existing
  dispatch masking test still passes.
- Audit ``project_path`` is correct; a route coverage test.

References
----------

- :doc:`0015-secret-references-and-envelope-encryption`
- :doc:`0064-access-rules-and-authorization`
- :doc:`0076-project-scoped-schedules-and-webhooks`
- ARCHITECTURE sections 9 and 12.4
