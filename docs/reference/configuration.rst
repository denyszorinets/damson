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
     - Required. Treated as a secret and never logged.
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
       neither TLS nor marked ``X-Forwarded-Proto: https`` by a proxy.
   * - ``controller.internal_url``
     - controller, dev
     - empty
     - URL other controllers use to reach this one; stored in
       ``controller_instances``.
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
     - ``4``
     - Concurrent executions, at least 1.

Example:

.. code-block:: toml

   [log]
   level = "debug"

   [database]
   driver = "postgres"
   dsn = "postgres://damson@db/damson"

   [controller]
   listen = "0.0.0.0:8080"
