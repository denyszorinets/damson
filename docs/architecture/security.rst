Security
========

Security design summary. The authoritative rules are in :doc:`design` §9, §12
and §18.

Trust boundaries
----------------

.. mermaid::

   flowchart LR
     subgraph clients["Untrusted network"]
       browser["Browser (OIDC session)"]
       machine["CI / sync (service token or JWT)"]
       hook["Webhook caller (hook token)"]
     end
     subgraph ctrl["Controller tier"]
       api["API + UI"]
       internal["Internal endpoints<br/>(HMAC cluster secret)"]
     end
     subgraph data["Data"]
       pg[("PostgreSQL<br/>encrypted secrets, hashed tokens")]
       bao[("OpenBao (optional)")]
     end
     subgraph zone["Network zone"]
       agent["Agent (pool token)"]
       targets["Targets"]
     end
     browser & machine & hook -->|HTTPS| api
     api --> pg & bao
     api <--> internal
     agent -->|wss + pool token| api
     agent -->|SSH host key policy, TLS| targets

Authentication
--------------

.. list-table::
   :header-rows: 1
   :widths: 25 75

   * - Principal
     - Mechanism
   * - Human (UI)
     - OIDC authorization code + PKCE; server-side sessions in the database;
       HttpOnly, Secure, SameSite=Lax cookie.
   * - Machine (API)
     - Service-account bearer tokens (hashed, scoped by project subtree and
       role, optional expiry) or OIDC JWT access tokens.
   * - Agent
     - Pool token (hashed, revocable) on the WebSocket upgrade; optional mTLS.
   * - Webhook
     - 32-byte random token in the URL path, shown once, stored as SHA-256,
       rate-limited; ``/hooks/*`` paths are redacted in logs.
   * - Controller ↔ controller
     - HMAC with a cluster shared secret, on an internal listener.
   * - Bootstrap
     - Config CEL rule for admins plus a one-time recovery token.

Until OIDC (M8), the secrets API is guarded by one admin bootstrap token
(``dma_`` prefix), created with ``damson admin admin-token create``. Only its
SHA-256 hash is stored, in ``instance_settings``, and the API checks it on every
request. Pool tokens are not accepted there and the admin token cannot open an
agent connection. It is a single shared credential with no per-user identity;
rotate it by creating a new one, which kills the old one at once. M8 keeps it as
the local recovery token.

All tokens are random and stored hashed. See
:doc:`decisions/0017-oidc-authentication-and-cel-authorization-on-a-project-tree`.

Authorization
-------------

Roles ``viewer`` < ``runner`` < ``editor`` < ``admin``, granted by CEL rules
over OIDC claims attached to projects (inherited) and jobs (additive). The
effective role is the maximum of all matching rules. Every mutation and
trigger writes an audit event.

Secrets lifecycle
-----------------

.. mermaid::

   sequenceDiagram
     participant DB as Secret backend
     participant C as Controller (dispatch)
     participant A as Agent
     participant P as Plugin
     Note over C: spec and stored input contain only secret:// refs
     C->>DB: resolve refs in step input and node credentials
     DB-->>C: values (decrypted DEK/KEK or OpenBao)
     C->>A: step.execute + secret bundle (TLS, not persisted)
     Note over C: keeps only value hashes for re-masking
     A->>P: ExecRequest.Secrets resolver
     P-->>A: logs and outputs
     A->>A: mask values (raw, base64, URL, per line)
     A->>C: masked logs and outputs
     C->>C: re-mask defensively on ingest
     Note over A: bundle dropped when the execution ends

Rules (``CLAUDE.md`` hard rule 5):

- Never log, persist or return secret values; only refs are stored.
- Values exist only in the dispatch path and the agent's memory, plus
  short-lived 0600 credential files on tmpfs where a tool needs them.
- Any new code path touching a secret bundle needs a masking test.
- **Losing the KEK means losing every secret in the db backend.** Back it up
  separately from the database.

Authorization: with the ``db`` backend a job may use secrets whose scope equals
its project path or is an ancestor of it (prefix on a ``/`` boundary). Siblings,
children and other projects are denied. The scope ``_inventory`` is allowed only
as the credential of the node a step targets, never as a literal in step input
(that part waits for inventory, :issue:`113`). The dispatcher is authoritative
and checks every ref before reading any secret; a denied ref fails the execution
permanently as ``unknown secret``, naming only the ref, so existence is not
revealed. A ``#field`` that the secret lacks fails permanently as unavailable.
API spec validation applies the same rule to literal refs and reports the path
only. ``--dev-nodes`` stays unauthorized (ADR-0034).

See :doc:`decisions/0015-secret-references-and-envelope-encryption`.

.. danger::

   The KEK of the ``db`` backend (``DAMSON_KEK`` or ``secrets.kek_file``) is
   the only way to read stored secrets. If it is lost, every secret is lost
   and there is no recovery. Back it up separately from the database, and
   keep it out of the config file, the logs and database backups.

For local development only, ``damson dev`` generates a 0600 key file next to
the SQLite database when no KEK is configured
(:ref:`reference-config-dev-kek`); ``damson controller`` never does.

Memory handling: the DEK and plaintext byte slices are zeroed after use, but
the Go strings in the decrypted field map cannot be wiped; they stay in the
controller's memory until garbage collected. Bundle values are dropped from
the dispatch request after the send, which shortens but does not eliminate
that window.

Each ciphertext is bound to its scope, name and version (and the wrap to the
``kek_id``) through authenticated data, so copying it to another row fails to
decrypt. A controller refuses to start with a KEK that does not match the
stored canary. See :doc:`decisions/0042-db-secret-backend-keyring`.

Other controls
--------------

- TLS everywhere; plain ``ws://`` only with ``--insecure-dev``.
- SSH host keys are ``strict`` by default; ``tofu`` stores observed keys
  (fenced by ``attempt_id``); ``insecure`` must be enabled in controller config.
- CEL runs with cost and result-size limits and exposes no I/O.
- Plugin inputs are validated against their schemas on the controller before
  dispatch; agents validate again.
- Image builds run only on dedicated build pools, since a Containerfile is
  arbitrary code.
- Agents report stale data that is discarded by fencing, so a partitioned agent
  cannot corrupt newer attempts.
