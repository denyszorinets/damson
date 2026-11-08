Backup and restore
==================

There is no built-in backup command. Back up the database with the tools of
your PostgreSQL service (``pg_dump`` for logical copies, base backups with WAL
archiving for point-in-time recovery) and keep the few things that are not in
the database somewhere else.

What to back up
---------------

.. list-table::
   :header-rows: 1
   :widths: 24 30 46

   * - What
     - Where it lives
     - Notes
   * - Everything durable
     - PostgreSQL
     - Projects, jobs and versions, runs and executions, schedules, webhooks,
       inventory nodes, access rules, service accounts and token hashes, pool
       definitions and token hashes, sessions, audit events, the encrypted
       secrets, and (with ``logs.store = db``) the log chunks.
   * - The KEK
     - ``DAMSON_KEK`` or ``secrets.kek_file``
     - **Not** in the database. Without it the ``db`` secret backend cannot be
       read and there is no recovery. Keep copies in at least two places
       outside the database backup (a password manager, an HSM or KMS-wrapped
       store). Keep every key that a backup in your retention window needs,
       including the key before a rotation (:doc:`upgrade`).
   * - Cluster secret, pool tokens, admin token
     - Environment, Secrets, password manager
     - Re-creatable: generate new ones (:doc:`upgrade`). Keeping them avoids
       reconfiguring controllers and agents after a disaster.
   * - S3 log objects
     - The bucket from ``logs.s3``
     - Only with ``logs.store = s3``. See below.
   * - OpenBao data
     - OpenBao
     - Only with ``secrets.backend = openbao``. Damson stores no secret values
       then; back up OpenBao with its own snapshot procedure.
   * - Configuration
     - ``controller.toml``, ``agent.toml``, Helm values, unit drop-ins
     - Keep it in version control. It holds no credentials if you followed
       :doc:`hardening`.

Nothing else is durable. Controllers keep no state in memory that matters, and
agents are stateless apart from a spool of unacknowledged log lines.

Database
--------

- Take a backup before every upgrade (:doc:`upgrade`).
- Use point-in-time recovery if you cannot afford to lose recent runs. A
  logical dump is consistent for the whole database in one transaction with
  ``pg_dump``:

  .. code-block:: console

     $ pg_dump --format=custom --file=damson-$(date +%F).dump \
         "postgres://damson@pg.example.com/damson?sslmode=verify-full"

- The database includes River's job tables (``river_*``). Restore the whole
  database, not a subset of tables.
- With ``logs.store = db`` the log chunks make the database grow with the log
  volume, and the backups with it. There is no run or log retention setting
  yet; only ``audit.retention_days`` prunes audit events. Plan the size, or
  move logs to S3.

S3 logs
-------

With ``logs.store = s3`` the database stores only the chunk index; the
content is in the bucket (:doc:`/reference/log-store`).

- Enable bucket versioning, or replication, if you need to survive deletion
  of objects. On a versioned bucket Damson's deletes only add delete markers,
  so purged data stays as noncurrent versions until a lifecycle rule expires
  them.
- Add a lifecycle rule for the ``logs/`` prefix as a backstop for orphaned
  objects.
- Restoring the database to an older point leaves objects in the bucket that
  nothing refers to; they are harmless. Restoring the bucket to an older point
  than the database leaves chunks that the database lists but the bucket lacks.
  The log stream shows these as gaps and logs ``log stream: chunk unreadable``.
  Back up the database and the bucket to similar points in time.
- Queued deletes (``log_object_deletes``) are in the database. A controller
  without the ``logs.s3`` block leaves them queued (:doc:`troubleshooting`).

Restore
-------

#. Stop every controller and every agent. Agents that keep running while the
   database goes back in time hold attempts the restored database may not
   know. How they behave across a restore is not covered by the tests.
#. Restore the database (or recover it to a point in time) into an empty
   PostgreSQL database.
#. Put back the KEK that matches the backup, the cluster secret and the
   configuration.
#. Start one controller with ``--auto-migrate``. If the backup is from an
   older version, it migrates forward. A KEK that does not match stops the
   start with an error naming the two ``kek_id`` values
   (:doc:`troubleshooting`).
#. Check ``/readyz``, then start the remaining controllers and the agents.
#. Runs that were in flight at the time of the backup continue where the
   database left off: their leases expire, the reaper marks their executions
   ``lost`` and the engine applies the job's retry policy.

Restore drill
~~~~~~~~~~~~~

Run this once before go-live and after major changes, on a scratch environment.

- Restore the latest database backup to a new PostgreSQL database.
- Start a controller with a copy of the KEK from your escrow (not the live
  one). It must start without a KEK error.
- Open a secret-using job's detail and trigger a run on a test agent. The
  dispatch must succeed (this proves the secrets decrypt).
- Open the logs of an old run, with ``logs.store = s3`` too. Chunks must read
  without gaps.
- Write down how long it took and how old the backup was.

RPO and RTO
-----------

Damson itself adds no recovery mechanism, so both numbers are those of your
database service.

- **RPO** is the age of the newest restorable database state: minutes with WAL
  archiving, up to a day with a nightly dump. Work done after that state is
  gone, including runs, their logs and audit events.
- **RTO** is the time to restore the database plus the time for the
  controllers to start (the migration, if any, and the first ``/readyz``).
  Agents reconnect on their own with exponential backoff up to 30 seconds.
- For a failure that does not lose the database (a controller or an
  availability zone), you do not restore anything: another controller takes
  over (:doc:`ha`). Use a replicated PostgreSQL with automatic failover for the
  rest.
