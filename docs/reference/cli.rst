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
deferred. Besides ``--config`` and ``--auto-migrate``, ``--insecure-dev`` (or
``controller.insecure_dev``) allows plain ``ws://`` agent connections; ``dev``
implies it.

``controller`` and ``dev`` open the configured database, serve the HTTP API and
web UI on ``controller.listen`` (:doc:`http-api`) and shut down gracefully on
a signal. ``--auto-migrate`` applies pending migrations at start; ``dev``
always does. Without it, a controller serves ``/healthz`` but reports
``/readyz`` 503 until the database is migrated. A database that cannot be
opened exits with status 1.

``agent`` connects to ``agent.controller_url`` with the pool token and keeps
the connection alive (:doc:`agent`). It exits with status 1 when the
controller refuses it in a way retrying cannot fix: an unknown or revoked
token, a bad request, a pool conflict or an unsupported protocol version.

``dev`` also starts an agent in the same process. At startup it creates the
``agent.pool`` pool (``default``) if missing, revokes the dev tokens of
earlier runs, issues a fresh token and hands it straight to the agent over
loopback. The token is never printed or logged. The agent appears connected
a moment after the HTTP server starts; ``dev`` stops the agent before the
controller on shutdown.

.. _cli-dev-nodes:

Dev-only nodes and secrets
--------------------------

``--dev-nodes <file>`` loads inline nodes and secret files from a JSON file so
that ``ssh`` steps can run before inventory and the secret store exist (M4;
:doc:`/architecture/decisions/0034-dev-only-inline-nodes-and-secrets`). It is a
flag only: there is no config key and no environment variable. ``dev`` accepts
it; ``controller`` accepts it only with ``--insecure-dev``; ``agent`` rejects
it (usage error, exit 2). A file that cannot be loaded fails startup with exit
status 1. Startup logs a WARN naming the file path and the node names.

.. code-block:: json

   {
     "version": 1,
     "nodes": {
       "sshd": {
         "address": "127.0.0.1",
         "port": 2222,
         "transport": "ssh",
         "os": "linux",
         "attributes": {},
         "credential": "secret://dev/sshd",
         "host_key": "ssh-ed25519 AAAA...",
         "host_key_policy": "strict"
       }
     },
     "secrets": {
       "secret://dev/sshd": {"username": "demo", "private_key_file": "/abs/path/id_ed25519"},
       "secret://dev/token": {"value_file": "/abs/path/token.txt"}
     }
   }

- ``version`` must be ``1``. Unknown fields are rejected.
- A node has ``address``, ``port`` (default 22), ``transport``, ``os``,
  ``attributes``, ``credential`` (a ref under ``secrets``), ``host_key`` and
  ``host_key_policy`` (``strict``, ``tofu`` or ``insecure``). No handler stores
  ``tofu`` host keys yet; use ``strict`` with a pinned ``host_key``.
- A secret is keyed by its ref without ``#field``. ``username`` may be inline.
  Every other field is given as ``<field>_file``, an absolute path. Secret
  values come only from files, which must be regular, mode 0600 or stricter and
  at most 64 KiB. They are read at every dispatch and never cached.
- A step referencing an unknown node or ref fails permanently; the message
  names only the node or ref.

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

``damson admin help`` (also ``-h`` or ``--help``) prints the list of resources
and actions to stdout and exits 0. Running ``damson admin`` with missing or
unknown arguments prints the same text to stderr and exits 2.

``token create`` prints the plaintext token **exactly once** on stdout (the bare
token, or a JSON object with ``--json``) and a reminder to store it on stderr.
Only its SHA-256 hash is stored, so a lost token cannot be recovered; create a
new one and revoke the old one. Treat the token as a secret and do not log it.

Exit status is 0 on success, 1 for operational errors (unmigrated database,
unknown pool or token, duplicate pool, already-revoked token) and 2 for usage
errors (missing or extra arguments, missing ``--pool`` or ``--name``).
