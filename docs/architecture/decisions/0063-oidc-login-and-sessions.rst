ADR-0063: OIDC login and sessions
==================================

:Status: Accepted
:Date: 2026-11-08

Issue :issue:`204`. Implements the UI authentication of ARCHITECTURE section
12.2.

Context
-------

Until now the admin routes took one shared token and the job and run routes were
open (ADR-0060). Users need to sign in through an identity provider, and any
controller must serve any request.

Decision
--------

**Libraries.** ``github.com/coreos/go-oidc/v3`` (Apache-2.0) and
``golang.org/x/oauth2`` (BSD-3-Clause), with ``go-jose`` v4 (Apache-2.0)
as a transitive dependency.

**Provider.**

- Discovery is lazy and retried, so a controller starts while the IdP is down.
- Only asymmetric signing algorithms are accepted (RS*, PS*, ES*, EdDSA, taken
  from the provider metadata; default RS256), never ``none`` or HS*.
- Claims come from the id token only.
- Verification uses the injected clock, and the nonce is checked.

**Login.** ``GET /auth/login?return_to=<path>`` stores a row in
``oidc_logins`` (``state_hash``, ``binding_hash``, nonce, PKCE verifier,
``return_to``, expiry in 10 minutes), sets the pre-login cookie
``__Host-damson_login`` (the binding; HttpOnly, Secure, SameSite=Lax, Path=/)
and redirects to the IdP.

**Callback.** ``GET /auth/callback`` first consumes the row
(``DELETE ... WHERE state_hash = ? AND expires_at > now``), so a state works
once, on any controller. It then requires the cookie whose hash equals
``binding_hash`` (this blocks login CSRF), exchanges the code with PKCE,
creates a session with a fresh random token, sets ``__Host-damson_session``
(12 hours) and redirects to ``return_to``. ``return_to`` must be a path on this
site (a single leading ``/``, no ``//``, backslash or control characters),
otherwise ``/``.

Failures are problem+json: 401 for an invalid, expired or replayed state, a
missing binding or a bad id token; 502 when the IdP is unavailable. The log
holds the reason, never token content.

**Logout.** ``POST /auth/logout`` (cookie and CSRF header) deletes the session,
clears the cookie and returns ``{"end_session_url": ...}`` built from the
provider's ``end_session_endpoint`` with ``client_id`` and
``post_logout_redirect_uri``. No ``id_token_hint`` is stored.

**Sessions.** Table ``sessions``: ``id``, ``token_hash`` (SHA-256 hex, unique),
``sub``, ``email``, ``name``, ``claims`` (JSON, at most 64 KiB; when larger it
is trimmed to ``sub``, ``email``, ``name`` and the groups claim),
``instance_admin`` (the ``admin_rule`` verdict at login), ``created_at``,
``last_seen_at`` (touched at most once a minute) and ``expires_at``. Lifetime
is 12 hours absolute and 2 hours idle. Validity is read from the database on
every request and never cached, so a logout on one controller applies on all at
once.

**Cleanup.** The retention duty (leader) deletes expired sessions and logins
when login is on. It needs no fencing, because those rows were already
unusable. The duty now also starts when OIDC is on while
``audit.retention_days`` is 0.

**CSRF.** A cookie-authenticated unsafe request needs ``X-CSRF-Token`` equal to
base64url(HMAC-SHA256(key = session cookie value, ``"damson-csrf"``)). It is
stateless and ``GET /api/v1/me`` returns it. When present, ``Sec-Fetch-Site``
must be ``same-origin`` or ``none``, and ``Origin`` must have the request's
host. A request with an ``Authorization`` header never uses the cookie: bearer
beats cookie, and an invalid token is 401.

**Anonymous access and admins.** With ``auth.oidc.issuer`` set, every operation
needs a principal except healthz, readyz, ``/openapi``, list-secret-types,
``/auth/*``, ``/hooks/{token}`` and agent connect. ``auth.require_token`` is
then ignored. Admin routes accept the admin token or an instance-admin session
(actor ``user:<sub>``). A logged-in user who is not an instance admin gets 403
on admin routes and on job and run routes until access rules land (#205); any
login may read agents, pools and plugins. Service tokens are unchanged
(ADR-0060). The ``dma_`` admin token stays as the local recovery token. It is
not printed at start; ARCHITECTURE section 12.2 is amended accordingly.

**Admin rule.** ``auth.oidc.admin_rule`` is CEL over ``claims`` and ``user``
(``sub``, ``email``, ``name``, ``groups``). It is compile-checked at startup. An
evaluation error grants nothing and logs a warning without claim values.

**Test double.** ``internal/auth/oidctest`` is a fake provider on a real HTTP
server: discovery, JWKS, an authorize endpoint that approves by ``login_hint``,
a token endpoint with PKCE and client secret checks, and knobs to produce bad
tokens.

Consequences
------------

- No refresh tokens in M8: claims are frozen at login and change at the next
  login, so a role revoked at the IdP lasts up to 12 hours. ARCHITECTURE
  section 12.2 ("refreshed on token refresh") is amended.
- OIDC JWT bearer tokens for machine-to-machine are deferred; service tokens
  cover it.
- Group access rules and their edits arrive with #205.


Hardening and known limits
==========================

- ``auth.oidc.issuer`` and ``auth.oidc.redirect_url`` must be ``https``: over plain
  http anyone on the network path could swap the signing keys and forge a login
  for any user, including an admin. ``http`` is accepted only with
  ``auth.cookie_insecure`` or ``controller.insecure_dev`` (development).
- ``GET /auth/login`` needs no login and writes a row, so it is limited per
  controller (20 a second, bursts of 40; 429 with ``Retry-After``). Expired logins
  are deleted by the retention duty, which now runs whenever login is on.
- A new login deletes the session the browser presented, so a replaced session does
  not linger until it goes idle.
- The client secret is masked in the startup config log.
- Until access rules (#205), any logged-in principal, including a non-admin user and
  a project-scoped viewer token, can read the agent, pool and plugin metadata
  endpoints. They hold no secrets.
- A session is checked when a request starts. An open event stream (run events,
  execution logs) keeps streaming after a logout or a revoked token until the client
  disconnects; this matches how token revocation already behaves (ADR-0060).
