.. _reference-secrets-openbao:

OpenBao secret backend
======================

``secrets.backend = openbao`` keeps secrets in OpenBao (or Vault) KV v2 instead
of the Damson database
(:doc:`/architecture/decisions/0068-openbao-secret-backend`). References,
authorization by project, delivery to agents and masking work as with the
``db`` backend. Settings are in :doc:`configuration`.

Requirements
------------

- The ``mount`` must be a **KV v2** secrets engine. Damson does not create it.
- The backend takes no KEK. Setting ``DAMSON_KEK`` or ``secrets.kek_file`` with
  ``secrets.backend = openbao`` is a configuration error, ``damson admin
  rotate-kek`` refuses, and ``damson dev`` always uses ``db``.
- ``address`` must be ``https`` (scheme and host only, no path) unless
  ``allow_plain_http`` is set. ``tls_ca_file`` adds trusted roots; verification
  cannot be disabled. Redirects are refused. An address with credentials
  (userinfo) is rejected.
- With a plain ``http://`` address and ``HTTP_PROXY`` set, the token goes to the
  proxy in clear text. Plain http is opt-in; prefer ``https``.
- ``mount``, ``prefix`` and ``auth.mount`` are validated at start (1 to 8
  segments of ``[A-Za-z0-9][A-Za-z0-9._-]*``). ``prefix`` must not be empty.

A secret ``secret://infra/billing/deploy-key`` is stored at
``<mount>/data/<prefix>/infra/billing/deploy-key`` (default
``secret/data/damson/infra/billing/deploy-key``). The type and field names are
kept in the KV ``custom_metadata`` as ``damson_type`` and
``damson_field_<name>``.

Policy
------

A controller that only reads secrets (``read_only = true``):

.. code-block:: text

   path "secret/data/damson/*" {
     capabilities = ["read"]
   }
   path "secret/metadata/damson/*" {
     capabilities = ["list", "read"]
   }

Managed mode (the Damson API and web UI create, update and delete secrets) also
needs:

.. code-block:: text

   path "secret/data/damson/*" {
     capabilities = ["create", "update", "read"]
   }
   path "secret/metadata/damson/*" {
     capabilities = ["list", "read", "create", "update", "delete"]
   }

``max_versions`` is a setting of the KV mount (or per secret in OpenBao);
Damson does not manage it. Deleting a secret through Damson removes all its
versions.

Authentication
--------------

.. code-block:: toml

   [secrets]
   backend = "openbao"

   [secrets.openbao]
   address = "https://bao.example.com:8200"
   read_only = true

   [secrets.openbao.auth]
   method = "approle"
   role_id_file = "/etc/damson/bao-role-id"
   secret_id_file = "/run/secrets/bao-secret-id"

AppRole takes ``role_id`` or ``role_id_file``, and ``secret_id_file`` or the
environment variable ``DAMSON_OPENBAO_SECRET_ID``. The auth mount defaults to
``approle``.

.. code-block:: toml

   [secrets.openbao.auth]
   method = "kubernetes"
   role = "damson"

The Kubernetes method reads the service account token from
``service_account_token_file`` (default
``/var/run/secrets/kubernetes.io/serviceaccount/token``) at every login. The
auth mount defaults to ``kubernetes``.

The ``token`` method takes ``token_file`` (re-read on re-login) or
``DAMSON_OPENBAO_TOKEN``. ``DAMSON_OPENBAO_TOKEN`` and
``DAMSON_OPENBAO_SECRET_ID`` are environment-only; they are never TOML keys and
are masked in the redacted config.

Damson renews its token at two thirds of the lease and logs in again when
renewal fails. A ``403`` does not log in at once: Damson first calls
``auth/token/lookup-self`` with the failed token. Only if that also answers
``403`` or ``401`` does it log in again (one login for any number of concurrent
callers) and retry once. If the token is valid, the ``403`` is a policy denial
and the secret is unavailable (permanent), so denied refs do not burn AppRole
``secret_id`` uses or tokens. If the re-login fails with an outage, the
transient error is returned. A refresh that fails transiently keeps the current
token and retries in about 30 s. On shutdown Damson revokes its login tokens (best
effort, 5 s).

Read-only mode
--------------

With ``read_only = true`` secrets are managed in OpenBao only. The API
answers ``409`` (problem type ``conflict``) to ``PUT`` and ``DELETE``, and ``--dev-nodes`` with secrets
refuses to start.

Cache
-----

``cache_ttl`` (default ``0``, off; maximum ``30s``) caches secrets on the
dispatch path in controller memory, up to 1024 entries.

- A write or delete through the same controller evicts the entry after every
  attempt, also when it fails or times out, and invalidates reads in flight
  (one global generation; entries of other keys stay).
- A change made through another controller or directly in OpenBao can be served
  stale for up to the TTL.
- A cached read does not reach OpenBao's audit log. Damson's ``secret.read``
  audit event is still written for every dispatched attempt.
- Go strings cannot be zeroed; expiry drops the references and the garbage
  collector frees them.

Audit
-----

``secret.create``, ``secret.update`` and ``secret.delete`` events (type, KV
version, field names, never values) are written **after** the change succeeded,
not in one transaction. A write without an actor is refused before any request.
If appending the event fails the error says "the secret changed but the audit
event was not recorded" and an ERROR is logged (action, scope, name, actor),
because the change cannot be rolled back. If the data version was written but
the metadata write fails, the new version is still audited (``secret.update``
with that version) and the error is returned. Changes made directly in OpenBao are audited by
OpenBao's audit device.

Migration
---------

Secrets in the Damson database are not copied. Recreate them in OpenBao (through
the Damson API or in OpenBao directly) before switching. A canary left in the
database only draws a warning at start; the old secrets are not served.

Secrets written directly in OpenBao, without Damson's metadata, appear as
``generic`` secrets without field names until they are saved once through
Damson; the next ``Put`` of any type adopts such a secret. A metadata-only entry
(for example from ``bao kv metadata put``) is not a secret: reads answer not
found and a create (``if-version`` 0) works. Listing keeps reading past such
entries and soft-deleted ones, so pages are never short. Values must be strings that satisfy the field and size rules of the
secret grammar; any other value makes the secret unavailable to dispatch. Oversized (over 2
MiB) or malformed answers are also permanent errors, not retried.

Behaviour during outages
------------------------

- **At start.** OpenBao rejecting the credentials (``400``, ``401``, ``403`` or
  ``404`` on login), an unreadable credential file, or a failing certificate
  check refuses to start. A network error or ``5xx`` starts the controller
  degraded with an ERROR log; it retries the login with a 1 s to 30 s backoff.
  There is no KV probe at start.
- **While unavailable.** Dispatch of steps with secrets requeues, and the
  secrets API answers ``503``.
- **Requests.** ``GET`` and ``LIST`` retry twice on network errors, ``429`` and
  ``5xx``. Writes are never retried automatically; a write that fails may need
  to be repeated by the caller. The request timeout is ``request_timeout``
  (default ``10s``).
