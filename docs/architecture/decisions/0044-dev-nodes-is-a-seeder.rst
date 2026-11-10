ADR-0044: --dev-nodes is a seeder into the inventory and the secret store
=========================================================================

:Status: Accepted
:Date: 2026-11-05
:Supersedes: :doc:`0034-dev-only-inline-nodes-and-secrets`

Context
-------

:doc:`0034-dev-only-inline-nodes-and-secrets` served nodes, secrets and
``targets`` from a JSON file through a separate path, without secret
authorization, and left open whether ``devnodes`` is removed or becomes a
seeder. M4 now has the inventory (:doc:`0043-inventory-nodes`), the sync core
(:issue:`116`) and the db secret backend (:doc:`0042-db-secret-backend-keyring`).
Keeping a second, unauthorized path means dev setups test something other than
what production runs (:issue:`115`).

Decision
--------

Seeding
~~~~~~~

- The flag still loads a JSON file (``version`` 1; nodes with ``address``,
  ``port``, ``transport``, ``os``, ``pool``, ``tags``, ``attributes``,
  ``credential``, ``host_key``, ``host_key_policy``; and ``secrets``). At
  startup, after the secret backend is ready, it **seeds** the file.
- Nodes go through the inventory sync core as source ``devfile`` with
  ``external_id`` equal to the node name. Devfile nodes no longer in the file
  are pruned. A node of another source with the same name makes the start fail
  with a conflict naming the owner.
- Secrets are written through the db backend.
- From then on dispatch, targets, validation and ref authorization use the real
  stores exactly as without the flag. The file-backed ``NodeSource``,
  ``SecretSource`` and ``Targets`` and the unauthorized dev path are deleted.

Refs and secret files
~~~~~~~~~~~~~~~~~~~~~

- Refs follow the real grammar and authorization: node credentials should be
  ``secret://_inventory/<name>``, step secrets ``secret://<project>/<name>``
  (for example ``secret://default/token``).
- Breaking: files with ``secret://dev/...`` no longer work. Spec validation
  returns ``422`` for the literal ref and dispatch fails with ``unknown
  secret``. A node's credential no longer has to be defined in the file; its
  existence is checked at dispatch.
- An optional literal key ``type`` (default ``generic``) sets the secret type.
  Only ``username`` may be a literal; every other field is ``<field>_file``, an
  absolute path read at **seed** time (regular file, mode 0600 or stricter, at
  most 64 KiB), no longer at dispatch.

Idempotence
~~~~~~~~~~~

- Each start compares stored and file values in constant time and writes a new
  secret version only when a value changed. A new secret is created with
  ``if_version`` 0; on a version conflict from a concurrent seeder it re-reads.
  Unchanged nodes keep their version.
- Secrets are never pruned: there is no ownership column. Delete them through
  the API or UI.
- A secret type that differs from the stored one fails startup naming the ref.
- A file with secrets but no KEK fails startup (``damson dev`` makes its own
  key; a controller needs ``DAMSON_KEK``). A file on an unmigrated database
  fails.

Targets and host keys
~~~~~~~~~~~~~~~~~~~~~

- ``targets`` come from the inventory (enabled nodes in scope) as in
  ARCHITECTURE 5.5. Superseded in part by :doc:`0045-frozen-run-targets`: they
  are no longer read live but frozen into the run at trigger.
- Host key policies are not exempt. ``insecure``, and ``tofu`` without a pinned
  ``host_key``, are seeded but refused at dispatch until :issue:`118`. Superseded by
  :doc:`0046-tofu-host-key-recording`: only ``insecure`` is refused now. Startup
  logs a WARN naming such nodes, and a WARN for ``_inventory`` secrets that no
  node uses as credential.

Logging
~~~~~~~

Startup logs a WARN with the file's node names and an INFO with the
created, updated, deleted and unchanged counts and the refs written. Values are
never logged.

Alternatives Considered
-----------------------

- **Keep the file-backed sources.** Rejected: a second path that skips
  authorization and validation.
- **Remove the flag.** Rejected: dev and tests need a one-file way to get nodes
  and secrets in.
- **Prune secrets too.** Rejected: without an ownership column a prune could
  delete secrets made through the API.

Decision Criteria
-----------------

Dev must exercise the production path; the seeder must be safe to run on every
start and by several controllers at once.

Rationale
---------

Seeding through the sync core and the db backend reuses their validation,
versioning and authorization, so there is nothing dev-specific to maintain.

Consequences
------------

Positive
~~~~~~~~

- One dispatch path, one authorization path, for dev and production.
- Restarts are idempotent and do not bump versions needlessly.

Negative
~~~~~~~~

- Breaking file format change (``secret://dev/...`` refs, ``_file`` read at
  seed time).
- A restart re-reads the files; a secret changed on disk overwrites a value
  edited through the API.

Risks
~~~~~

- Stale seeded secrets remain after being removed from the file.
- Several controllers on one Postgres database given different files take turns
  overwriting each other on every restart (pruning devfile nodes, adding secret
  versions). Give them the same file, or pass ``--dev-nodes`` to one only.
- A node credential outside the ``_inventory`` scope (for example an old
  ``secret://dev/sshd``) seeds fine but only jobs of that project can use it;
  startup warns about it. Without a secret backend (a controller without a KEK)
  a node with a credential fails at dispatch with "no secret source is configured".
- The seeder runs after the single-controller guard on SQLite, so a second
  process that is refused never changes the running controller's inventory.

Follow-up
~~~~~~~~~

- :issue:`117`: frozen run targets. :issue:`118`: host key flow for ``tofu``.

Validation
----------

Seed tests run on SQLite and Postgres: create, unchanged restart, change,
prune, owner conflict, type mismatch, missing KEK, concurrent seeders, and
masking of values in logs.

References
----------

- :doc:`0034-dev-only-inline-nodes-and-secrets`
- :doc:`0038-for-each-lanes`
- :doc:`0042-db-secret-backend-keyring`
- :doc:`0043-inventory-nodes`
- :doc:`/reference/cli`
