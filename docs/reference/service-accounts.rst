.. _reference-service-accounts:

Service accounts and tokens
===========================

A service account is a named identity for automation. It holds API tokens, each
with its own project scope, role and optional expiry. Design:
:doc:`/architecture/decisions/0060-service-accounts-and-api-tokens`.

The service account routes need ``Authorization: Bearer <admin token>``; a
service token is rejected there.

Create an account
-----------------

.. code-block:: shell

   curl -sX POST http://localhost:8080/api/v1/service-accounts \
       -H "Authorization: Bearer $DAMSON_TOKEN" \
       -H 'Content-Type: application/json' \
       -d '{"name": "ci", "description": "CI pipeline"}'

``name`` is required and unique: lower-case letters, digits, ``_`` and ``-``,
starting with a letter, up to 63 characters.

Create a token
--------------

.. code-block:: shell

   curl -sX POST http://localhost:8080/api/v1/service-accounts/<account id>/tokens \
       -H "Authorization: Bearer $DAMSON_TOKEN" \
       -H 'Content-Type: application/json' \
       -d '{"name": "deploy", "project": "default", "role": "runner",
            "expires_at": "2027-01-01T00:00:00Z"}'

``name``, ``project`` and ``role`` are required. ``project`` is a project name;
empty covers every project. ``expires_at`` is optional and must be in the
future; after that instant the token gives 401. The answer is the token record
plus ``token`` (``dms_...``), which **is shown only in this answer**. Damson
stores only its SHA-256 hash.

Use a token
-----------

.. code-block:: shell

   curl -sX POST http://localhost:8080/api/v1/jobs/<job id>/runs \
       -H "Authorization: Bearer dms_..." \
       -H 'Content-Type: application/json' -d '{"options": {}}'

The run records ``trigger.actor`` as ``sa:<service account id>``.

Roles and scope
---------------

.. list-table::
   :header-rows: 1

   * - Role
     - Allowed
   * - ``viewer``
     - Read jobs, versions, graphs, runs, executions and logs; validate a spec.
   * - ``runner``
     - Everything ``viewer`` can, plus trigger and cancel runs.
   * - ``editor``
     - Everything ``runner`` can, plus create jobs and job versions.

An ``editor`` token can also ``POST /apply`` job files and unchanged
``project.json`` files for its scope (:doc:`jobs-as-code`); project changes
need ``admin``.

A service token is never an admin: secrets, nodes, audit, inventory sync,
service accounts and log purge need the admin token, schedules need a token role of
``editor`` (``viewer`` to read), webhooks need ``viewer`` to read and cannot be
created or revoked by a token, and
so do creating, updating and deleting projects and all access-rule routes
(they need ``admin``). A token with a role too low for a route gets ``403``;
one whose scope does not cover the target gets ``404``. See :doc:`access-rules`.

A scoped token covers its project and the projects below it. Creating a job and
listing runs without a ``job`` filter need a token with an empty ``project``,
because their project is not known in advance. Listing jobs checks the
``project`` query parameter (default ``default``).

List and revoke
---------------

.. code-block:: shell

   curl -s http://localhost:8080/api/v1/service-accounts/<account id>/tokens \
       -H "Authorization: Bearer $DAMSON_TOKEN"
   curl -sX DELETE http://localhost:8080/api/v1/tokens/<token id> \
       -H "Authorization: Bearer $DAMSON_TOKEN"

The list includes revoked tokens, with ``revoked_at``. Revoking is idempotent
and keeps the row; the token stops working at once. ``last_used_at`` is updated
at most once a minute. ``DELETE /service-accounts/<id>`` deletes the account
and all its tokens.

Answers
-------

- ``401`` with ``WWW-Authenticate: Bearer realm="damson"``: the token is
  invalid, expired, revoked, unknown or has the wrong prefix, or no token was
  sent while ``auth.require_token`` is ``true``.
- ``403``: the token's role is too low for the operation.
- ``404``: the target does not exist or is outside the token's scope; the two
  look the same.

Requiring tokens
----------------

When OIDC login is configured (``auth.oidc.issuer``), anonymous access ends and
``auth.require_token`` is ignored; see :doc:`authentication`.

Without OIDC the job, run and execution routes accept requests without a token. Set ``auth.require_token: true``
(:doc:`/reference/configuration`) to refuse them. A token that is sent is
checked whatever the setting.

Audit
-----

``service_account.create``, ``service_account.delete``,
``service_token.create`` and ``service_token.revoke`` are recorded (see
:doc:`http-api`). Details hold names, scope, role and expiry, never the token
or its hash.
