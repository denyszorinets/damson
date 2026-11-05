ADR-0042: The db secret backend: KEK keyring, envelope format and startup rules
================================================================================

:Status: Accepted
:Date: 2026-11-04

Context
-------

:doc:`0015-secret-references-and-envelope-encryption` chose envelope encryption
for the ``db`` backend and left the details open. M4 implements it
(:issue:`112`). The format is expensive to change once secrets are stored, and
a controller that starts with the wrong key must not silently write secrets
nobody can read.

Decision
--------

Key encryption key (KEK)
~~~~~~~~~~~~~~~~~~~~~~~~

- Exactly 32 bytes in standard base64.
- Sources: the environment variable ``DAMSON_KEK``, or the file named by the
  config key ``secrets.kek_file``. Setting both is a configuration error.
- The previous key, for rotation, comes from ``DAMSON_KEK_PREVIOUS`` or
  ``secrets.kek_previous_file``.
- The environment values are not TOML keys. They are never logged, and
  ``config.Redacted`` masks them.
- ``kek_id`` is ``hex(SHA-256("damson-kek-id/v1" || key))[:16]``. Only
  ``kek_id`` values appear in logs and errors.

Envelope
~~~~~~~~

- Every secret version gets a random 32-byte data encryption key (DEK).
- ``ciphertext`` is AES-256-GCM over the JSON object ``{field: value}`` with a
  random 12-byte nonce.
- ``wrapped_dek`` is a 12-byte wrap nonce followed by AES-256-GCM(KEK, DEK).
- Additional authenticated data (AAD) for the data is
  ``"damson-secret/v1\0" + scope + "\0" + name + "\0" + version``. The wrap
  uses the same AAD plus ``"\0" + kek_id``. A ciphertext copied to another row
  or version therefore fails to decrypt.
- The DEK and plaintext byte slices are zeroed after use. The Go strings in the
  returned field map cannot be zeroed; they live until garbage collected.

Canary and startup rules
~~~~~~~~~~~~~~~~~~~~~~~~

The ``instance_settings`` key ``secrets.kek_canary`` holds JSON
``{kek_id, nonce, ct}``: a fixed plaintext encrypted under AAD
``"damson-canary/v1"``. At controller start:

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - State
     - Result
   * - No KEK, no canary
     - Secrets are disabled, with a WARN. Dispatch of a step that uses a
       ``secret://`` ref fails permanently with "no secret source configured".
   * - No KEK, canary present
     - The controller refuses to start.
   * - KEK, no canary
     - The canary is stored. If a peer wins the ``CreateSetting`` race, its
       canary is verified with this key, so two controllers with different KEKs
       cannot both start.
   * - Canary opens with the active key
     - Start.
   * - Canary opens with the previous key
     - Start with a WARN: run ``damson admin rotate-kek``.
   * - Canary opens with neither
     - The controller refuses to start, naming ``kek_id`` values only.

``damson admin rotate-kek`` ships later in M4 (:issue:`119`).

Schema (migration 00010)
~~~~~~~~~~~~~~~~~~~~~~~~

- ``secrets``: ``id``, ``scope_path``, ``name``, ``type``, ``current_version``
  (the CAS counter), ``created_at``, ``updated_at``; unique on
  ``(scope_path, name)``.
- ``secret_versions``: ``secret_id``, ``version``, ``kek_id``, ``wrapped_dek``,
  ``nonce``, ``ciphertext``, ``field_names`` (JSON array), ``created_at``;
  primary key ``(secret_id, version)``; an index on ``kek_id`` for rotation.
- The last 10 versions are kept; older ones are pruned in the same
  transaction as the insert.
- Listing never selects the encrypted columns.

Reference grammar
~~~~~~~~~~~~~~~~~

``secret://<scope>/<name>[#<field>]``; the scope is all segments but the last.

- A scope segment matches ``[a-z0-9][a-z0-9-]{0,62}``; 1 to 8 segments, so a
  leading ``_`` is impossible. ``_inventory`` is allowed only as the whole
  scope.
- ``name`` matches ``[A-Za-z0-9][A-Za-z0-9._-]{0,127}`` and has no ``..``.
- ``field`` matches ``[a-z][a-z0-9_]{0,63}``.
- At most 512 bytes, ASCII only.

Types and limits
~~~~~~~~~~~~~~~~

- ``generic``: any fields.
- ``password``: ``username``, ``password`` (required).
- ``ssh_key``: ``username``, ``private_key`` (required, multiline),
  ``passphrase``.
- ``token``: ``token`` (required).
- At most 64 KiB per field, 256 KiB in total and 32 fields. The type of a
  secret cannot change.

Dispatch
~~~~~~~~

The db backend is the secret source when a KEK is configured and
``--dev-nodes`` is not given; ``--dev-nodes`` stays the only source when set
(:doc:`0034-dev-only-inline-nodes-and-secrets`).

.. note::

   Superseded in part: see :doc:`0044-dev-nodes-is-a-seeder`. ``--dev-nodes``
   now seeds the stores and is no longer a source.

- A missing secret fails the execution permanently (``ErrUnknownSecret``).
- A malformed ref, an unknown ``kek_id`` or a decryption failure fails it
  permanently with ``ErrSecretUnavailable``. The message names only the ref;
  the controller logs an ERROR with the ref and ``kek_id``.
- Any other error, such as the database being down, requeues the execution.
- Values live only in the ``step.execute`` bundle and are wiped after the
  send.

Out of scope
~~~~~~~~~~~~

The UI, ``rotate-kek`` (:issue:`119`) and controller re-masking on ingest
(:issue:`120`). The last remains an accepted gap, as in ADR-0034.

Refs are authorized by project scope (ARCHITECTURE 9.1, :issue:`114`); see
:doc:`security </architecture/security>`. The node-credential allowance for the
``_inventory`` scope waits for inventory (:issue:`113`), which does not exist
yet.

Alternatives Considered
-----------------------

- **One key for all secrets, no DEKs.** Rejected: rotation would re-encrypt
  every value, and one nonce space would be shared by all secrets.
- **No canary; fail on the first undecryptable secret.** Rejected: a
  controller with a wrong key would start and break runs one at a time, and
  two controllers with different keys could both write.
- **Derive the KEK from a passphrase.** Rejected for v1: operators supply a
  random key, which avoids a KDF and its parameters.

Decision Criteria
-----------------

- A wrong or missing key must stop the controller, not corrupt data.
- Ciphertext must be bound to its row and version.
- The format must allow rotation without decrypting values.
- No new infrastructure or dependency beyond the Go standard library.

Rationale
---------

Envelope encryption lets rotation re-wrap 32-byte DEKs only. Binding the
scope, name and version into the AAD stops row swapping by anyone with write
access to the database. The canary turns a key mismatch into a startup error
and, through the ``CreateSetting`` race, makes a cluster agree on one key.

Consequences
------------

Positive
~~~~~~~~

- A wrong key is detected at start, naming ``kek_id`` values only.
- Ciphertext copied between rows or versions does not decrypt.
- Rotation needs no plaintext handling.

Negative
~~~~~~~~

- **Losing the KEK loses every secret in the db backend. There is no
  recovery.** Back the key up separately from the database.
- Decrypted values are Go strings in the controller's memory until garbage
  collected; they cannot be wiped.
- Until :issue:`120`, the controller does not re-mask on ingest.

Risks
~~~~~

- A leaked KEK together with a database dump exposes all secrets. Keep the key
  out of the config file, logs and backups of the database.
- ``if_version`` is a compare-and-swap on the version number, and a recreated
  secret starts again at version 1. A client that read version 1, while another
  caller deleted and recreated the secret, can therefore overwrite the new
  secret with ``if_version`` 1. The window needs a delete in between and the
  admin token is one shared credential, so the risk is accepted for now; keeping
  version numbers rising across recreation would need a schema change.
- Request bodies pass through the HTTP stack's buffers before the handler clears
  the decoded fields; those buffers are not scrubbed. The same limit as for Go
  strings applies (see Decision).
- Secret writes and deletes are not audited yet (ARCHITECTURE 19).

Follow-up
~~~~~~~~~

- ``damson admin rotate-kek`` (:issue:`119`), the UI and controller
  re-masking (:issue:`120`).

Until M8 the admin token is one shared credential: there are no per-user
identities or audit of who used it. Rotate it by creating a new one.

Admin token and API
~~~~~~~~~~~~~~~~~~~

The secrets HTTP API is guarded by an admin bootstrap token, the interim form
of the local recovery token of ARCHITECTURE 12.2. It stays as the recovery
token when OIDC arrives in M8.

- ``damson admin admin-token create`` makes ``dma_`` plus base64url of 32 random
  bytes and prints it once. Only its SHA-256 hex is stored, in
  ``instance_settings`` key ``auth.admin_token_hash``. Creating again replaces
  the hash with a compare-and-swap update, so the old token dies at once;
  ``admin-token revoke`` sets the value empty.
- The API reads the hash on every request, so the database stays the truth and
  any controller serves any request. A missing or wrong token is a ``401``
  problem with ``WWW-Authenticate: Bearer realm="damson-admin"``, checked
  before the body is parsed. Pool tokens (``dmp_``) are never accepted, and the
  admin token is not accepted on ``/agent/v1/connect``.
- Routes: ``GET /api/v1/secrets`` (metadata only), ``PUT
  /api/v1/secrets/{scope}/{name}`` and ``DELETE`` on the same path; ``GET
  /api/v1/secret-types`` is public. Values are write-only and never returned
  (:doc:`/reference/http-api`). With no KEK configured the secrets routes answer
  ``503`` after authentication.
- ``damson admin kek generate`` prints a new KEK for the config and needs no
  database.

Validation
----------

Unit tests cover the keyring, the AAD binding (a copied ciphertext fails), the
canary matrix and the ref grammar. Store tests run on SQLite and Postgres.
Dispatch tests check the permanent and requeue outcomes and that values do not
appear in errors or logs.

References
----------

- :doc:`0015-secret-references-and-envelope-encryption`
- :doc:`0034-dev-only-inline-nodes-and-secrets`
- :doc:`/architecture/security`
- :doc:`/development/store`
