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
   * - ``audit.retention_days``
     - controller, dev
     - ``0``
     - Days to keep audit events; ``0`` keeps them forever. A positive value lets
       the leader of the retention duty delete older events and record an
       ``audit.prune`` event per batch (at most 36500). Environment variable
       ``DAMSON_AUDIT__RETENTION_DAYS``. See
       :doc:`/architecture/decisions/0054-audit-retention`.
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
     - Let the ``ssh`` and ``transfer`` plugins connect to nodes with host key policy
       ``insecure``.
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
