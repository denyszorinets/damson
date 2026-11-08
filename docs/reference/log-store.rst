.. _reference-log-store:

Log store
=========

Log chunks are stored in the database (``db``, the default) or in an
S3-compatible bucket (``s3``). Keys are in :ref:`reference-config-logs`;
the design is :doc:`/architecture/decisions/0067-s3-log-store-and-deferred-deletes`.

Example (MinIO)
---------------

.. code-block:: toml

   [logs]
   store = "s3"

   [logs.s3]
   endpoint = "https://minio.internal:9000"
   region = "us-east-1"
   bucket = "damson-logs"
   prefix = "prod"
   path_style = true
   tls_ca_file = "/etc/damson/minio-ca.pem"
   access_key_id = "damson"
   secret_access_key_file = "/run/secrets/minio-secret"

Pass the secret through ``DAMSON_LOGS__S3__SECRET_ACCESS_KEY`` or the file form,
not the config file in version control.

Bucket and permissions
----------------------

The bucket must exist; Damson does not create it. The credentials need
``s3:PutObject``, ``s3:GetObject``, ``s3:DeleteObject`` and ``s3:ListBucket``
(the prefix delete lists, then deletes). With ``logs.store = s3`` a controller
probes the bucket at start and refuses to start if it is unreachable.

Switching backends
------------------

- ``db`` to ``s3``: set the ``logs.s3`` block and ``logs.store = "s3"``, restart
  all controllers. New chunks go to the bucket; old chunks stay readable from
  the database.
- ``s3`` back to ``db``: set ``logs.store = "db"`` and **keep the** ``logs.s3``
  **block**. The bucket stays registered for reads, so chunks written there remain
  readable (the start log warns that ``s3`` is read-only).
- Removing the block, or changing the bucket, endpoint or prefix, abandons the
  objects written under the old values. Their chunks show as gaps in the log
  stream, with a warning that names the backend.

Purging logs
------------

``DELETE /api/v1/executions/{id}/logs`` (:doc:`http-api`) removes database
chunks in one transaction with the audit event and queues the execution's
object prefix. A sweeper on every controller deletes the objects, normally
within a minute, retrying with backoff (up to an hour) while the bucket is
unavailable. When a new attempt claims an execution, the previous attempt's
objects are queued the same way after a 2 minute grace. Nothing is queued
when the execution had no chunks in an external store, so db-only installs
queue nothing. A controller without the ``logs.s3`` block leaves queued entries
in place and logs a warning; keep the block on every controller (including
during a rolling restart) until the queue is drained.

Orphans and lifecycle rules
---------------------------

Objects can stay behind when an upload succeeded but recording its chunk
failed and the cleanup also failed, or when a stale writer uploads after the
grace period. They are removed
when the execution's logs are purged. Until per-project log retention exists,
add a bucket lifecycle rule (for example, expire objects under the
``logs/`` prefix after your retention period) as a backstop.

Versioned buckets
-----------------

On a bucket with versioning, deletes only add delete markers. Purged data stays
as noncurrent versions until a lifecycle rule expires noncurrent versions; add
one if purge must free the storage.

Encryption
----------

Traffic uses TLS (``allow_plain_http`` is for trusted networks only). Server-side
encryption is off by default; set ``logs.s3.sse`` to ``AES256`` or ``aws:kms``
(with ``sse_kms_key_id``). Chunks are already masked by the agent before they
are stored.
