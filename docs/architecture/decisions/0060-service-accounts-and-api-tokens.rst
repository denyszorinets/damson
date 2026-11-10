ADR-0060: Service accounts, tokens and interim API authentication
=================================================================

:Status: Accepted
:Date: 2026-11-07

Issue :issue:`196`. Implements the service account tokens of ARCHITECTURE
section 12.2.

Context
-------

Automation needs credentials that are scoped, expire and can be revoked. Until
OIDC arrives in M8, the job, run and execution routes have been open to anyone
who reaches the controller. This change adds tokens and a way to enforce them
without breaking existing callers.

Decision
--------

**Tables.** Migration 00016 adds ``service_accounts(id, name unique,
description, created_at, updated_at)`` and ``service_tokens(id,
service_account_id, name, token_hash unique, project, role, expires_at,
revoked_at, last_used_at, created_at)``. ``service_account_id`` is a foreign
key with cascade delete, so deleting an account deletes its tokens.

**Token.** ``dms_`` plus base64url of 32 random bytes. It is returned once in
the create response; only its SHA-256 is stored.

**Scope and role.**

- ``project`` is a project name. Empty means every project. A scoped token
  covers the project and its descendants by the path prefix ``scope/``; today
  projects are flat until M8.
- ``role`` is ``viewer`` < ``runner`` < ``editor``. ``runner`` triggers and
  cancels; ``editor`` changes jobs.
- A service token never has the admin role. Admin routes (secrets, nodes,
  audit, inventory sync, service accounts, webhooks, schedules, log purge)
  keep requiring the admin bootstrap token and reject service tokens.

**Lifecycle.** ``expires_at`` is optional, must be in the future, and expiry is
exclusive. ``DELETE /tokens/{id}`` revokes, is idempotent, and keeps the row.
``last_used_at`` is written best effort, at most once a minute.

**Enforcement.** One middleware runs over a table of operations: validate-job,
create-job, list-jobs, get-job, create-job-version, list-job-versions,
get-job-version, get-job-version-graph, trigger-run, list-runs, get-run,
get-run-graph, stream-run-events, cancel-run, get-execution,
stream-execution-logs. The required role is ``viewer`` for reads and validate,
``runner`` for trigger-run and cancel-run, and ``editor`` for create-job and
create-job-version.

**Interim rule.** Without an ``Authorization`` header the request passes unless
``auth.require_token`` is ``true`` (default ``false``). This is the interim
until OIDC in M8, because these routes have been open to anyone who reaches the
controller. A token that is sent is always checked, whatever the setting:

- The admin token may do everything.
- A service token needs a high enough role (403 otherwise) and a scope covering
  the target project (otherwise 404, the same as a missing target).
- ``create-job`` and ``GET /runs`` without a ``job`` filter need an
  unrestricted token (empty scope), because the middleware does not know their
  project. ``list-jobs`` checks ``project`` (default ``default``). Other routes
  resolve the project from the job, run or execution id.
- An invalid, expired, revoked, unknown or wrong-prefix token gives 401 with
  ``WWW-Authenticate: Bearer realm="damson"``.

**Trigger actor.** A run started with ``POST /jobs/{id}/runs`` records
``trigger.actor`` as ``sa:<service account id>`` or
``admin-token:<first 12 hex of the hash>`` when a token was sent.

**API (admin token).** ``GET`` and ``POST /service-accounts``; ``GET`` and
``DELETE /service-accounts/{id}``; ``GET`` and ``POST
/service-accounts/{id}/tokens``; ``DELETE /tokens/{id}``.

**Audit.** ``service_account.create``, ``service_account.delete``,
``service_token.create`` and ``service_token.revoke`` (target types
``service_account`` and ``service_token``). Details are names, scope, role and
expiry; never the token or its hash. Token events carry the project as
``project_path``.

Alternatives considered
-----------------------

- OIDC JWT access tokens: planned for M8.
- Scope stored on the account instead of on each token: less flexible, since
  one account could not hold tokens of different reach.
- Per-route middleware: easy to forget on a new route; the operation table
  keeps the rules in one place.

Consequences
------------

- With the default ``auth.require_token: false`` the job, run and execution
  routes stay open to callers that send no token. Operators who want them
  closed set it to ``true``.
- A token is a bearer secret; losing it means revoking and creating a new one.
- Scope by path prefix is trivially correct while projects are flat. It stays
  correct in M8 only if a project's name is its path and projects are not
  renamed; otherwise the token must store the project id.
- With ``auth.require_token: true`` the embedded web UI stops working for job
  and run pages: it sends no token there, and the live streams use
  ``EventSource``, which cannot set headers. The UI authenticates with OIDC
  sessions in M8; until then use the option for API-only deployments.
- ``auth.require_token`` covers the job, run and execution routes only. The
  agent, pool and plugin read routes stay open (they are listed on purpose in
  the completeness test), and the admin routes keep needing the admin token.
- A live stream (run events, execution logs) is authorized when it opens. A
  token revoked or expired afterwards does not end a stream already open; the
  stream ends with the run or when the client disconnects. A request
  authorized just before a revoke can still start one run, attributed to the
  token's account.
- A test lists every API operation and fails for one that is neither governed,
  admin-guarded nor named as public, so a new route cannot be left open by
  accident.

References
----------

- :doc:`0053-audit-events`
- :doc:`0059-webhooks`
- ARCHITECTURE sections 12.2, 12.4 and 14
