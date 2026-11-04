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
     - Let the ``ssh`` plugin connect to nodes with host key policy
       ``insecure``.

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
