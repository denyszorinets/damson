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
     - Manage agent pools, pool tokens, the admin token and the secret encryption key (see :ref:`cli-admin`).
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

``dev`` also needs a KEK for secrets. When none is configured it generates one
in ``<sqlite database file>.kek`` (mode 0600) and reuses it on later starts;
``controller`` never does. See :ref:`reference-config-dev-kek`.

.. _cli-dev-nodes:

Dev-only nodes and secrets
--------------------------

``--dev-nodes <file>`` seeds nodes and secrets from a JSON file into the
inventory and the secret store at startup
(:doc:`/architecture/decisions/0044-dev-nodes-is-a-seeder`). It is a flag only:
there is no config key and no environment variable. ``dev`` accepts it;
``controller`` accepts it only with ``--insecure-dev``; ``agent`` rejects it
(usage error, exit 2). A file that cannot be loaded or seeded fails startup with
exit status 1.

After the secret backend is ready, the file is seeded:

- Nodes go through inventory sync as source ``devfile`` (``external_id`` is the
  node name). Devfile nodes that are no longer in the file are deleted. A node of
  another source (for example ``manual``) with the same name makes the start fail
  with a conflict naming the owner.
- Secrets are written to the ``db`` backend. They are never pruned: delete them
  through the API or UI. A secret whose ``type`` differs from the stored one
  fails startup naming the ref.
- From then on dispatch, ``targets`` (``for_each: "targets"``,
  ``spec.targets.selector``), spec validation and ref authorization use the real
  stores exactly as without the flag. ``attributes`` are not secret, so a
  ``secret://`` value in them is rejected.
- A file with secrets needs a KEK. ``damson dev`` makes its own key (see
  :ref:`reference-config-dev-kek`); a ``controller`` needs ``DAMSON_KEK``.
  Without one, or on a database that is not migrated, startup fails.

.. code-block:: json

   {
     "version": 1,
     "nodes": {
       "sshd": {
         "address": "127.0.0.1",
         "port": 2222,
         "transport": "ssh",
         "os": "linux",
         "pool": "default",
         "tags": ["dev"],
         "attributes": {},
         "credential": "secret://_inventory/sshd",
         "host_key": "ssh-ed25519 AAAA...",
         "host_key_policy": "strict"
       }
     },
     "secrets": {
       "secret://_inventory/sshd": {"type": "ssh_key", "username": "demo", "private_key_file": "/abs/path/id_ed25519"},
       "secret://default/token": {"token_file": "/abs/path/token.txt"}
     }
   }

- ``version`` must be ``1``. Unknown fields are rejected.
- A node has ``address``, ``port`` (default 22), ``transport``, ``os``,
  ``pool``, ``tags``, ``attributes``, ``credential``, ``host_key`` and
  ``host_key_policy`` (``strict``, ``tofu`` or ``insecure``).
- Refs follow the real grammar and authorization: node credentials should be
  ``secret://_inventory/<name>`` and step secrets ``secret://<project>/<name>``.
  A node's credential does not have to be defined in the file; its existence is
  checked at dispatch.
- A secret is keyed by its ref without ``#field``. The optional literal key
  ``type`` is the secret type (default ``generic``). Only ``username`` may be a
  literal; every other field is ``<field>_file``, an absolute path read at seed
  time (a regular file, mode 0600 or stricter, at most 64 KiB), not at dispatch.
- Re-seeding is idempotent. Each start compares the stored and file values and
  writes a new secret version only when a value changed; unchanged nodes keep
  their version. Concurrent seeders are safe. A restart re-reads the files, so
  a value changed through the API is overwritten if the file differs.
- Host key policies are not exempt: ``insecure`` is seeded but refused at
  dispatch. ``tofu`` without a pinned ``host_key`` records the first key it
  sees (:doc:`/architecture/decisions/0046-tofu-host-key-recording`). Startup warns
  about such nodes and about ``_inventory`` secrets that no node uses.
- Startup logs a WARN with the file's node names and an INFO with the created,
  updated, deleted and unchanged counts and the refs written, never values.

.. warning::

   Breaking change: files using ``secret://dev/...`` refs no longer work. The
   literal ref fails spec validation with ``422`` and dispatch fails with
   ``unknown secret``. Rename the refs as above.

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
   * - ``damson admin admin-token create``
     - Create the admin token for the secrets API. Replaces any earlier one.
   * - ``damson admin admin-token revoke``
     - Remove the admin token; the secrets API then refuses every request.
       An error if there is none.
   * - ``damson admin kek generate``
     - Print a new key encryption key for the db secret backend.
   * - ``damson admin rotate-kek [--json]``
     - Re-wrap every stored secret key under the new KEK (see :ref:`cli-rotate-kek`).

``damson admin help`` (also ``-h`` or ``--help``) prints the list of resources
and actions to stdout and exits 0. Running ``damson admin`` with missing or
unknown arguments prints the same text to stderr and exits 2.

``token create`` prints the plaintext token **exactly once** on stdout (the bare
token, or a JSON object with ``--json``) and a reminder to store it on stderr.
Only its SHA-256 hash is stored, so a lost token cannot be recovered; create a
new one and revoke the old one. Treat the token as a secret and do not log it.

``admin-token create`` makes a token of the form ``dma_`` plus base64url of 32
random bytes and prints it **exactly once** on stdout (``--json`` gives
``{"token": ...}``) with a reminder on stderr. Only its SHA-256 hash is stored
(``instance_settings`` key ``auth.admin_token_hash``). Creating again replaces
it and the old token stops working immediately; that is how you rotate it. It
is one shared token until OIDC arrives in M8, which keeps it as the recovery
token.

``kek generate`` prints a new 32-byte base64 key on stdout and the ``kek_id``
plus a warning on stderr. It needs no database or config. Keep the key safe:
losing it loses every secret in the db backend
(:doc:`/architecture/decisions/0042-db-secret-backend-keyring`).

.. _cli-rotate-kek:

Rotating the KEK
~~~~~~~~~~~~~~~~

``rotate-kek`` takes ``--config`` and ``--json``. The new key comes from
``DAMSON_KEK`` (or ``secrets.kek_file``) and the old key from
``DAMSON_KEK_PREVIOUS`` (or ``secrets.kek_previous_file``). It first checks
the KEK canary: a canary that opens with neither key is an error naming the
``kek_id`` values, and no canary is an error too (the instance has no
secrets). It then pages through the secret versions not wrapped under the
active key, in batches of 100, unwraps each data key with the old KEK and wraps
it under the new one. The data ciphertext is not touched. Each row is updated
only if it still has the old ``kek_id``, so a concurrent change is never
overwritten. The canary is re-wrapped last, and only if nothing failed.

The command is crash-safe and resumable: rows under the old and the new key
both decrypt, because controllers hold both keys. A rerun finds only what is
left, and a run with nothing left changes nothing. Secret versions written
meanwhile already use the active key. Rows wrapped under a key that is neither
the active nor the previous one count as failed, the exit status is 1 and the
canary stays where it is.

It prints counts and the active ``kek_id`` only, never keys or values:
``rewrapped``, ``skipped``, ``failed``, ``stragglers`` and ``canary_moved``.

Runbook:

#. Run ``damson admin kek generate`` to get the new key.
#. Roll the controllers to hold both keys: ``DAMSON_KEK`` is the new key and
   ``DAMSON_KEK_PREVIOUS`` the old one. They start with a WARN to run
   ``rotate-kek`` while the canary is still under the old key.
#. Make sure no controller still runs with the old key alone (a controller
   only checks the key at start, and one on the old key keeps writing versions
   nobody else can read once the old key is dropped).
#. Run ``damson admin rotate-kek`` with the same environment. It repeats full
   passes until one finds nothing to re-wrap, moves the canary, and scans once
   more.
#. When it reports the canary moved and ``stragglers`` is 0, remove
   ``DAMSON_KEK_PREVIOUS`` and restart the controllers at your pace. If it
   reports stragglers, a controller is still on the old key: restart it with
   the new key, run ``rotate-kek`` again, and keep the old key until a run
   reports none.

A controller that holds only the new key cannot start between step 2 and the
canary move. Keep the old key until every row is rotated: a database backup
taken before the rotation needs the old key to be read.

Exit status is 0 on success, 1 for operational errors (unmigrated database,
unknown pool or token, duplicate pool, already-revoked token) and 2 for usage
errors (missing or extra arguments, missing ``--pool`` or ``--name``).
