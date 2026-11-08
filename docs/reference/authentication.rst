Authentication (OIDC login)
===========================

Set ``auth.oidc.issuer`` to enable sign-in through an OpenID Connect provider
(ADR-0063). Without it, behaviour is as in :doc:`service-accounts`.

Once it is set, anonymous access ends: every operation needs a principal except
``healthz``, ``readyz``, ``/openapi``, list-secret-types, ``/auth/*``,
``/hooks/{token}`` and agent connect. ``auth.require_token`` is ignored.

Endpoints
---------

.. list-table::
   :header-rows: 1

   * - Endpoint
     - Behaviour
   * - ``GET /auth/login?return_to=<path>``
     - Stores the login state and redirects to the IdP. ``return_to`` must be a
       path on this site (one leading ``/``, no ``//``, backslash or control
       characters), else ``/``.
   * - ``GET /auth/callback``
     - Consumes the state (once, on any controller), checks the binding
       cookie, exchanges the code with PKCE, creates the session and redirects
       to ``return_to``.
   * - ``POST /auth/logout``
     - Needs the session cookie and CSRF header. Deletes the session, clears
       the cookie and returns ``{"end_session_url": ...}`` from the provider's
       ``end_session_endpoint``.
   * - ``GET /api/v1/me``
     - ``kind`` (``anonymous``, ``admin_token``, ``service_account`` or
       ``user``), ``actor``, ``sub``, ``email``, ``name``, ``groups``,
       ``instance_admin`` and ``csrf_token``.

Login failures are problem+json: ``401`` for an invalid, expired or replayed
state, a missing binding cookie or a bad id token; ``502`` when the IdP is
unavailable.

Cookies
-------

- ``__Host-damson_login``: pre-login binding, valid for the 10 minute login.
- ``__Host-damson_session``: the session token, 12 hours.

Both are HttpOnly, Secure, SameSite=Lax and Path=/. ``auth.cookie_insecure``
(development only) drops ``Secure`` and the ``__Host-`` prefix.

Sessions last 12 hours absolute and 2 hours idle. There are no refresh tokens:
claims are fixed at login, so a role revoked at the IdP lasts up to 12 hours.

CSRF
----

A cookie-authenticated unsafe request needs ``X-CSRF-Token`` with the
``csrf_token`` value from ``/me``. If ``Sec-Fetch-Site`` is sent it must be
``same-origin`` or ``none``, and ``Origin`` must have the request's host. A
request with an ``Authorization`` header never uses the cookie; an invalid
token there is ``401``.

Browser
-------

The web UI calls ``GET /api/v1/me`` when it loads. With OIDC configured, a
visitor without a session gets ``401`` there and the browser is sent to
``/auth/login?return_to=<path and query>`` (a path on this site only), and
back to that page after sign-in. Without OIDC, ``/me`` answers ``200`` as
``anonymous`` or ``admin_token`` and nothing changes: the admin pages keep their
admin token prompt, and the job and run pages work without a login. With
``auth.require_token=true`` and no OIDC, the job and run pages fail, because
the UI has no login then; use OIDC or service tokens through the API.

The session cookie is HttpOnly, so scripts never read it. The generated API
client adds ``X-CSRF-Token`` (from ``/me``) to every non-``GET`` request. A
logged-in user's admin pages (nodes, secrets, service accounts, audit) use the
cookie and send no ``Authorization`` header. A user who is not an instance admin
sees no admin links; the server still answers ``403``. The user menu shows the
email and **Log out**, which posts ``/auth/logout`` and then goes to the
provider's ``end_session_url`` (or the home page). The **Projects** page manages
the project tree, access rules and node scope
(:doc:`/development/web-ui`).

Who may do what
---------------

- Admin routes accept the admin token or an instance-admin session (audit
  actor ``user:<sub>``). Instance admin is the ``admin_rule`` verdict at login.
- A logged-in user who is not an instance admin gets ``403`` on admin routes.
  Job, run, execution, project and access-rule routes are decided by the role
  that access rules give the user (:doc:`access-rules`); no role is ``404``, too
  low a role is ``403``. Any login may read agents, pools and plugins.
- Service tokens keep their role and project scope (:doc:`service-accounts`). The ``dma_`` admin token stays as the local
  recovery token; create it with ``damson admin admin-token create`` (it is not
  printed at start).

Configuration
-------------

Environment names look like ``DAMSON_AUTH__OIDC__ISSUER``.

.. list-table::
   :header-rows: 1

   * - Key
     - Meaning
   * - ``auth.oidc.issuer``
     - Issuer URL. Enables login.
   * - ``auth.oidc.client_id``
     - Client id.
   * - ``auth.oidc.client_secret``, ``auth.oidc.client_secret_file``
     - Client secret (a secret), or a file holding it.
   * - ``auth.oidc.redirect_url``
     - Must end in ``/auth/callback``.
   * - ``auth.oidc.scopes``
     - Default ``profile,email,groups``; ``openid`` is always added.
   * - ``auth.oidc.groups_claim``
     - Default ``groups``.
   * - ``auth.oidc.admin_rule``
     - CEL over ``claims`` and ``user`` (``sub``, ``email``, ``name``,
       ``groups``), for example ``'damson-admins' in user.groups``. Compiled at
       startup. An evaluation error grants nothing and logs a warning without
       claim values.
   * - ``auth.oidc.post_logout_redirect_url``
     - Sent to the IdP as ``post_logout_redirect_uri``.
   * - ``auth.cookie_insecure``
     - Development only; see Cookies.
