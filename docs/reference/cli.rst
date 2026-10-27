.. _reference-cli:

Command line
============

Damson ships as one binary, ``damson``. The role is chosen by subcommand
(:doc:`/architecture/decisions/0002-single-go-binary-with-controller-and-agent-roles`).

.. list-table::
   :header-rows: 1

   * - Command
     - Purpose
   * - ``damson controller``
     - Run the controller role.
   * - ``damson agent``
     - Run the agent role.
   * - ``damson dev``
     - Run a controller and an agent in one process with SQLite.
   * - ``damson version``
     - Print the version.

``controller``, ``agent`` and ``dev`` accept ``--config <path>`` (or the
``DAMSON_CONFIG`` environment variable) naming a TOML file; see
:doc:`configuration`. They log JSON to stderr and stop on SIGINT or SIGTERM.
Invalid configuration prints every problem and exits with status 1; usage
errors exit with status 2. Per-key flags (for example ``--listen``) are
deferred; ``--config`` and ``--auto-migrate`` are the only flags today.

``controller`` and ``dev`` open the configured database, serve the HTTP API and
web UI on ``controller.listen`` (:doc:`http-api`) and shut down gracefully on
a signal. ``--auto-migrate`` applies pending migrations at start; ``dev``
always does. Without it, a controller serves ``/healthz`` but reports
``/readyz`` 503 until the database is migrated. A database that cannot be
opened exits with status 1.

.. note::

   Until later M0 issues land, ``agent`` loads and validates configuration,
   logs startup and waits for a signal; it does not connect yet.
