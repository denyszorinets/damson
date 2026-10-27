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
   * - ``damson admin``
     - Manage agent pools and pool tokens (see :ref:`cli-admin`).
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

.. _cli-admin:

Admin commands
--------------

``damson admin`` works directly against the configured database; it does not
need a running controller. Every action accepts ``--config <path>`` (or
``DAMSON_CONFIG``) and ``--json`` for machine-readable output; the default is a
table. The database must already be migrated (run ``damson controller
--auto-migrate`` or ``damson dev`` once); otherwise the command exits with an
error saying so.

.. list-table::
   :header-rows: 1

   * - Command
     - Purpose
   * - ``damson admin pool create <name> [--description d]``
     - Create an agent pool. A duplicate name is an error.
   * - ``damson admin pool list``
     - List pools.
   * - ``damson admin token create --pool <name> --name <token-name>``
     - Create a pool token for agents to connect with.
   * - ``damson admin token list --pool <name>``
     - List a pool's tokens: id, name, created, last used and revoked times.
       Revoked tokens stay listed. Hashes are never shown.
   * - ``damson admin token revoke <token-id>``
     - Revoke a token. A revoked token can no longer authenticate.

``token create`` prints the plaintext token **exactly once** on stdout (the bare
token, or a JSON object with ``--json``) and a reminder to store it on stderr.
Only its SHA-256 hash is stored, so a lost token cannot be recovered; create a
new one and revoke the old one. Treat the token as a secret and do not log it.

Exit status is 0 on success, 1 for operational errors (unmigrated database,
unknown pool or token, duplicate pool, already-revoked token) and 2 for usage
errors (missing or extra arguments, missing ``--pool`` or ``--name``).
