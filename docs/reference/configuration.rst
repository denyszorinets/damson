.. _reference-config:

Configuration
=============

Configuration is read from, in increasing precedence:

#. built-in defaults,
#. an optional TOML file (``--config`` or ``DAMSON_CONFIG``),
#. ``DAMSON_*`` environment variables.

Unknown keys are rejected, and all validation problems are reported together,
each naming the key and its environment variable. The loader is
``internal/config`` (koanf).

Environment variables are ``DAMSON_`` plus the key uppercased, with ``.``
written as a double underscore: ``agent.controller_url`` is
``DAMSON_AGENT__CONTROLLER_URL``.

.. list-table::
   :header-rows: 1
   :widths: 25 12 15 48

   * - Key
     - Roles
     - Default
     - Notes
   * - ``log.level``
     - all
     - ``info``
     - ``debug``, ``info``, ``warn`` or ``error``. Logs are slog JSON.
   * - ``database.driver``
     - controller, dev
     - ``sqlite``
     - ``sqlite`` or ``postgres`` (``dev`` allows only ``sqlite``).
   * - ``database.dsn``
     - controller, dev
     - ``file:damson.db``
     - Required. Treated as a secret and never logged. On Postgres, ``idle_in_transaction_session_timeout`` in the URL overrides the default of 30 seconds (see :doc:`/development/store`).
   * - ``controller.listen``
     - controller, dev
     - ``:8080``
     - ``host:port``.
   * - ``controller.insecure_dev``
     - controller, dev
     - ``false``
     - Allow plain ``ws://`` agent connections (also ``--insecure-dev``; always
       on for ``dev``). Development only: the pool token crosses the network
       in clear text. Without it the gateway refuses connections that are
       TLS (or, with ``controller.trust_forwarded_proto``, marked
       ``X-Forwarded-Proto: https`` by a proxy).
   * - ``controller.tls_cert_file``
     - controller
     - empty
     - PEM certificate file, leaf first, then the chain. Set together with
       ``controller.tls_key_file`` (otherwise startup fails with "must be set
       together"). The listener then serves HTTPS and WSS, TLS 1.2 or newer,
       ALPN ``h2`` and ``http/1.1`` (the agent WebSocket uses HTTP/1.1). The
       ``dev`` role refuses these keys ("is not supported by the dev role (its
       agent connects over ws://)"). Environment variable ``DAMSON_CONTROLLER__TLS_CERT_FILE``
       (:doc:`/operations/install`).
   * - ``controller.tls_key_file``
     - controller
     - empty
     - PEM private key for the certificate. A secret: mode 0640. The pair is
       re-checked at most every 10 seconds by modification time and size and
       reloaded without restart; a broken replacement is ignored and the
       previous one keeps serving. Environment variable
       ``DAMSON_CONTROLLER__TLS_KEY_FILE``.
   * - ``controller.trust_forwarded_proto``
     - controller, dev
     - ``false``
     - Honor ``X-Forwarded-Proto: https`` from a TLS-terminating proxy when
       deciding whether an agent connection is secure. Enable only behind a
       proxy that sets or strips the header; otherwise clients can forge it.
   * - ``controller.internal_url``
     - controller, dev
     - empty
     - Address of this controller's internal listener as other controllers
       reach it (for example ``http://damson-1.internal:8081``); stored in
       ``controller_instances``. Required when a cluster secret is set. Only
       scheme, host and port: ``http`` or ``https``, no credentials, path,
       query or fragment.
   * - ``controller.internal_listen``
     - controller
     - ``:8081``
     - ``host:port`` of the internal listener that serves other controllers
       (:doc:`/architecture/decisions/0040-internal-controller-endpoints`).
       Started only when a cluster secret is set; never in ``dev``. Plain HTTP:
       keep it on a private network, because log lines cross it unencrypted.
   * - ``controller.cluster_secret``
     - controller
     - none
     - Shared secret that authenticates requests between controllers (at
       least 32 bytes; env ``DAMSON_CONTROLLER__CLUSTER_SECRET``). Required,
       together with ``internal_url``, with the postgres driver; SQLite needs
       none. Redacted in the effective configuration and never logged.
   * - ``controller.cluster_secret_file``
     - controller
     - none
     - File to read the secret from when ``cluster_secret`` is empty;
       surrounding whitespace is trimmed.
   * - ``controller.cluster_secret_previous``
     - controller
     - none
     - Old secret that is still accepted for verification (never used to sign),
       for rotation. At least 32 bytes; redacted.
   * - ``secrets.kek_file``
     - controller
     - none
     - File holding the key encryption key (KEK) of the ``db`` secret backend:
       32 bytes in standard base64, whitespace trimmed. Alternatively set
       ``DAMSON_KEK``. Setting both is a configuration error. See
       :ref:`reference-config-kek`.
   * - ``secrets.kek_previous_file``
     - controller
     - none
     - File holding the previous (old) KEK, used while running ``damson admin rotate-kek``. Alternatively set
       ``DAMSON_KEK_PREVIOUS``; not both.
   * - ``secrets.backend``
     - controller
     - ``db``
     - ``db`` or ``openbao``. See :doc:`secrets-openbao`. ``openbao`` takes no KEK.
   * - ``secrets.openbao.address``
     - controller
     - none
     - Required with ``openbao``. ``https://host:port``, scheme and host only, no userinfo.
   * - ``secrets.openbao.namespace``
     - controller
     - empty
     - Sent as ``X-Vault-Namespace``.
   * - ``secrets.openbao.mount``
     - controller
     - ``secret``
     - KV v2 mount path (1 to 8 segments of ``[A-Za-z0-9][A-Za-z0-9._-]*``).
   * - ``secrets.openbao.prefix``
     - controller
     - ``damson``
     - Path under the mount (same grammar as ``mount``); must not be empty.
   * - ``secrets.openbao.read_only``
     - controller
     - ``false``
     - Writes answer ``409``; secrets are managed in OpenBao.
   * - ``secrets.openbao.tls_ca_file``
     - controller
     - empty
     - Extra root certificates (PEM). Verification cannot be disabled.
   * - ``secrets.openbao.allow_plain_http``
     - controller
     - ``false``
     - An ``http`` address is refused without it. With ``HTTP_PROXY`` set the token goes to the proxy in clear text; prefer https.
   * - ``secrets.openbao.request_timeout``
     - controller
     - ``10s``
     - Per request; up to ``1m``.
   * - ``secrets.openbao.cache_ttl``
     - controller
     - ``0s``
     - Dispatch-path cache; ``0`` is off, maximum ``30s``.
   * - ``secrets.openbao.auth.method``
     - controller
     - none
     - ``token``, ``approle`` or ``kubernetes``. Settings of another method are an error.
   * - ``secrets.openbao.auth.token_file``
     - controller
     - none
     - Method ``token``: file holding the token, re-read on re-login. Alternatively env ``DAMSON_OPENBAO_TOKEN``; not both.
   * - ``secrets.openbao.auth.role_id``
     - controller
     - none
     - Method ``approle``: role id (redacted). Exactly one of ``role_id`` and ``role_id_file``.
   * - ``secrets.openbao.auth.role_id_file``
     - controller
     - none
     - Method ``approle``: file holding the role id.
   * - ``secrets.openbao.auth.secret_id_file``
     - controller
     - none
     - Method ``approle``: file holding the secret id. Alternatively env ``DAMSON_OPENBAO_SECRET_ID``; exactly one.
   * - ``secrets.openbao.auth.role``
     - controller
     - none
     - Method ``kubernetes``: role name (required).
   * - ``secrets.openbao.auth.mount``
     - controller
     - ``approle`` or ``kubernetes``
     - Mount path of the auth method (same grammar as ``mount``).
   * - ``secrets.openbao.auth.service_account_token_file``
     - controller
     - ``/var/run/secrets/kubernetes.io/serviceaccount/token``
     - Method ``kubernetes``: re-read at every login.
   * - ``audit.retention_days``
     - controller, dev
     - ``0``
     - Days to keep audit events; ``0`` keeps them forever. A positive value lets
       the leader of the retention duty delete older events and record an
       ``audit.prune`` event per batch (at most 36500). Environment variable
       ``DAMSON_AUDIT__RETENTION_DAYS``. See
       :doc:`/architecture/decisions/0054-audit-retention`.
   * - ``runs.retention_days``
     - controller, dev
     - ``0``
     - Days to keep finished runs; ``0`` keeps them forever. A positive value
       (at most 36500; negative is rejected) lets the leader of the retention
       duty delete succeeded, failed and canceled runs finished more than this
       many days ago, with their executions and logs. Deletion is permanent.
       Environment variable ``DAMSON_RUNS__RETENTION_DAYS``. See
       :doc:`/architecture/decisions/0074-run-retention`.
   * - ``engine.finally_grace``
     - controller, dev
     - ``2m``
     - How long ``finally`` blocks may keep running after a cancel or the
       job-level timeout (a Go duration, positive, at most 24h). The same value
       must be used on every controller of a cluster. See
       :doc:`/architecture/decisions/0037-groups-scoping-and-status`.
   * - ``agent.controller_url``
     - agent
     - none
     - Required; ``ws://`` or ``wss://``.
   * - ``agent.token``
     - agent
     - none
     - Required pool token. Never logged.
   * - ``agent.pool``
     - agent
     - ``default``
     - Agent pool name.
   * - ``agent.slots``
     - agent
     - ``0``
     - Concurrent executions (``max_concurrent`` in ``agent.hello``).
       ``0`` means the CPU count times four; otherwise at least 1.
   * - ``agent.drain_grace``
     - agent
     - ``20s``
     - How long a stopping agent (SIGTERM) waits for running steps before it
       cancels them. During the wait it takes no new work. ``0s`` cancels at
       once. Keep it plus 5 s below the platform's stop timeout.
   * - ``agent.spool_dir``
     - agent
     - empty
     - Directory of the log spool file. Empty means the system temp dir. The
       file holds masked job output in plaintext (mode 0600) and is unlinked
       as soon as it is created; stale ``damson-spool-*`` files are removed at
       start, so do not share the directory between agents.
   * - ``agent.spool_max_bytes``
     - agent
     - ``0``
     - Bound of the log spool, in bytes. ``0`` means 256 MiB.
   * - ``agent.allow_insecure_host_keys``
     - agent
     - ``false``
     - Let the ``ssh``, ``transfer`` and ``ansible`` plugins connect to nodes with
       host key policy ``insecure``, and the ``ansible``, ``buildah`` and ``helm``
       plugins clone ssh repositories without a known_hosts entry.
   * - ``agent.allow_insecure_winrm``
     - agent
     - ``false``
     - Let the ``winrm`` plugin use ``transport: http`` and
       ``insecure_skip_verify``. Environment variable
       ``DAMSON_AGENT__ALLOW_INSECURE_WINRM`` (:doc:`/reference/plugin-winrm`).
   * - ``agent.allow_plain_ftp``
     - agent
     - ``false``
     - Let the ``transfer`` plugin use ``protocol: ftp`` (clear text). Without it
       plain ftp is refused. Environment variable
       ``DAMSON_AGENT__ALLOW_PLAIN_FTP`` (:doc:`/reference/plugin-transfer`).
   * - ``agent.allow_insecure_git``
     - agent
     - ``false``
     - Let the ``ansible``, ``buildah`` and ``helm`` plugins clone from ``http://``, ``git://`` and
       ``file://`` URLs. Without it only ``https`` and ``ssh`` are accepted.
       Environment variable ``DAMSON_AGENT__ALLOW_INSECURE_GIT``
       (:doc:`/reference/plugin-ansible`).
   * - ``agent.allow_helm_in_cluster``
     - agent
     - ``false``
     - Let the ``helm`` plugin deploy with the agent's own Kubernetes service
       account (``cluster.in_cluster``). Without it such a step is refused.
       Environment variable ``DAMSON_AGENT__ALLOW_HELM_IN_CLUSTER``
       (:doc:`/reference/plugin-helm`).
   * - ``agent.allow_insecure_registry``
     - agent
     - ``false``
     - Let the ``buildah`` plugin run with ``tls_verify: false``. Without it such
       a step is refused. Environment variable
       ``DAMSON_AGENT__ALLOW_INSECURE_REGISTRY``
       (:doc:`/reference/plugin-buildah`).
   * - ``agent.allow_plain_s3``
     - agent
     - ``false``
     - Let the ``transfer`` plugin use an ``s3`` endpoint over plain ``http://``
       (clear text). Without it such an endpoint is refused. Environment variable
       ``DAMSON_AGENT__ALLOW_PLAIN_S3`` (:doc:`/reference/plugin-transfer`).
   * - ``agent.tls_ca_file``
     - agent
     - empty
     - PEM file of extra CA certificates trusted, in addition to the system
       roots, for ``ftps`` and ``https`` S3 endpoints of the ``transfer`` plugin
       and for ``winrm`` over https. Read at start (a change needs a restart); a
       bad file fails startup. Certificate verification cannot be turned off.
   * - ``agent.controller_ca_file``
     - agent
     - empty
     - PEM file of extra CA certificates trusted, in addition to the system
       roots, for the ``wss://`` connection to the controller (a private CA or a
       self-signed controller certificate). Environment variable
       ``DAMSON_AGENT__CONTROLLER_CA_FILE``. Read at start only (a change needs
       a restart). Certificate verification cannot be
       turned off. Distinct from ``agent.tls_ca_file``.
   * - ``logs.store``
     - controller, dev
     - ``db``
     - Where new log chunks are written: ``db`` or ``s3``. ``fs`` is refused
       ("not implemented"). See :ref:`reference-config-logs`.
   * - ``logs.s3.*``
     - controller, dev
     - off
     - S3-compatible log store, used when ``logs.s3.bucket`` is set. All keys
       in :ref:`reference-config-logs`.
   * - ``auth.require_token``
     - controller
     - ``false``
     - Require a Bearer token (the admin token or a service account token) on
       the job, run and execution routes. Off: a request without an
       ``Authorization`` header passes, as these routes were open before. A
       token that is sent is checked either way. Ignored when
       ``auth.oidc.issuer`` is set: login is then always required
       (:doc:`/reference/service-accounts`). Environment variable
       ``DAMSON_AUTH__REQUIRE_TOKEN``.
   * - ``auth.oidc.*``, ``auth.cookie_insecure``
     - controller
     - off
     - Browser login with an OpenID Connect provider (issuer, client id and
       secret, redirect URL, groups claim, admin rule). All keys and their
       defaults are in :doc:`/reference/authentication`.

.. _reference-config-logs:

Log store
---------

``logs.store`` chooses where new chunks go. Chunks already written stay
readable through their stored backend (``db`` or ``s3``). Operations guide:
:doc:`log-store`. Design: :doc:`/architecture/decisions/0067-s3-log-store-and-deferred-deletes`.

.. list-table::
   :header-rows: 1
   :widths: 28 14 58

   * - Key
     - Default
     - Notes
   * - ``logs.store``
     - ``db``
     - ``db`` or ``s3``. With ``s3`` the bucket is probed at start (10 s) and a
       failure refuses to start.
   * - ``logs.s3.endpoint``
     - empty (AWS)
     - ``http(s)`` URL with no user info, path, query or fragment. Validation errors name the key only and never echo the value.
   * - ``logs.s3.region``
     - empty
     - Bucket region.
   * - ``logs.s3.bucket``
     - empty
     - Required with ``logs.store = s3``. When set, ``s3`` is also registered
       for reads with ``logs.store = db``.
   * - ``logs.s3.prefix``
     - empty
     - Key prefix without leading or trailing ``/`` and without empty, ``.`` or
       ``..`` segments. Stored with a trailing ``/``.
   * - ``logs.s3.path_style``
     - ``false``
     - Path-style addressing (MinIO and most self-hosted servers).
   * - ``logs.s3.allow_plain_http``
     - ``false``
     - An ``http`` endpoint is refused without it. Use only on a trusted network.
   * - ``logs.s3.tls_ca_file``
     - empty
     - Extra root certificates (PEM). Verification cannot be disabled.
   * - ``logs.s3.access_key_id``
     - empty
     - Without it the credential chain is used: AWS environment variables, then
       IAM or IRSA.
   * - ``logs.s3.secret_access_key``
     - empty
     - Required with ``access_key_id`` unless the file form is used. Secret,
       masked in the redacted config. Environment variable
       ``DAMSON_LOGS__S3__SECRET_ACCESS_KEY``.
   * - ``logs.s3.secret_access_key_file``
     - empty
     - File holding the key (whitespace trimmed). Not together with
       ``secret_access_key``.
   * - ``logs.s3.session_token``
     - empty
     - Temporary credentials; needs ``access_key_id``. Secret, masked.
   * - ``logs.s3.sse``
     - empty
     - Server-side encryption: empty (off), ``AES256`` or ``aws:kms``.
   * - ``logs.s3.sse_kms_key_id``
     - empty
     - KMS key id; needs ``sse = aws:kms``.

.. _reference-config-kek:

Secret encryption key (KEK)
---------------------------

The ``db`` secret backend encrypts every secret with keys wrapped by one KEK
(:doc:`/architecture/decisions/0042-db-secret-backend-keyring`). Provide it
either in the environment variable ``DAMSON_KEK`` or in a file named by
``secrets.kek_file``, never both. The previous key uses ``DAMSON_KEK_PREVIOUS``
or ``secrets.kek_previous_file``; set it only while rotating, together with the
new key in ``DAMSON_KEK`` (:ref:`cli-rotate-kek`). The value is exactly 32 bytes in standard
base64. Generate one with:

.. code-block:: console

   $ openssl rand -base64 32

Keep the key out of the config file and the logs. ``DAMSON_KEK`` and
``DAMSON_KEK_PREVIOUS`` are not TOML keys; they are never logged and are masked
in the redacted config. Only a short ``kek_id`` is ever logged.

.. warning::

   Losing the KEK loses every secret in the ``db`` backend; there is no
   recovery. Back it up separately from the database.

``DAMSON_OPENBAO_TOKEN`` and ``DAMSON_OPENBAO_SECRET_ID`` (the ``openbao``
backend, :doc:`secrets-openbao`) are likewise environment-only, never TOML keys,
and masked in the redacted config. The KEK settings do not apply to the
``openbao`` backend; setting one with ``secrets.backend = openbao`` is an error.

Without a KEK and without stored secrets, secrets are disabled (a WARN at
start) and steps that use ``secret://`` refs fail. A controller without a KEK
refuses to start against a database that already holds secrets.

.. _reference-config-dev-kek:

Dev key file
~~~~~~~~~~~~

Only ``damson dev`` (never ``damson controller``) generates a KEK when none is
configured. It writes a 32-byte base64 key to ``<sqlite database file>.kek``
(for example ``damson.db.kek``) with mode 0600. The file is created atomically:
a private temp file in the same directory is synced and then hard-linked into
place, so there is never an empty or partial key file and two dev processes
starting together share one key. Later starts reuse the file. The startup log
carries the key file path and ``kek_id`` (never the key) and warns that losing
the file loses the dev secrets.

``DAMSON_KEK`` or ``secrets.kek_file`` wins. If a ``.kek`` file also exists, the
dev role logs a warning and ignores it.

The file is refused, and start fails naming the path, if it is a symlink or not
a regular file, if group or others can read it (the message says to ``chmod
600``), or if it is empty or does not hold a valid key.

If the key file is deleted while the database still holds secrets (the KEK
canary), nothing is wiped and no new key is made, because it would fit nothing:
the dev role refuses to start with a message naming the key file and the
database files ("restore it, or delete ... to start over"). Restoring the file
brings the instance back. ``rm damson.db*`` (which also removes the ``.kek``
file) resets key and data together. A key file that belongs to another database
(for example after copying the database alone) is refused with the same advice.

``damson controller``, also with ``--insecure-dev --dev-nodes`` on Postgres,
never generates a key, because each controller would make its own. Set
``DAMSON_KEK`` there.

Rotating the cluster secret
---------------------------

Verification accepts the current and the previous secret; signing uses the
current one. Rotate from secret A to B with two rolling restarts, so that
controllers never disagree on what they accept:

1. On every controller set ``cluster_secret = A`` and
   ``cluster_secret_previous = B``. All controllers now accept both.
2. On every controller set ``cluster_secret = B`` and
   ``cluster_secret_previous = A``. They now sign with B and still accept A
   from controllers not yet restarted.
3. Remove ``cluster_secret_previous``.

A failed request only costs the live tail (the log stream falls back to stored
chunks), never data.

Example:

.. code-block:: toml

   [log]
   level = "debug"

   [database]
   driver = "postgres"
   dsn = "postgres://damson@db/damson"

   [controller]
   listen = "0.0.0.0:8080"
