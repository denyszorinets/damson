Backup and restore
==================

There is no ``damson backup`` command. Two helper scripts wrap the database
tools (see Helper scripts below); for point-in-time recovery use the tools of
your PostgreSQL service (base backups with WAL archiving). Keep the few things
that are not in the database somewhere else.

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

Helper scripts
--------------

:repo:`deploy/backup/damson-backup.sh` and ``damson-restore.sh`` are POSIX shell
scripts. They are not in the offline bundle; take them from the source of your
release. They read the controller's own variables, ``DAMSON_DATABASE__DRIVER``
(``postgres``, the default, or ``sqlite``) and ``DAMSON_DATABASE__DSN``.

.. code-block:: console

   $ damson-backup.sh -o /var/backups/damson -f /etc/damson
   $ damson-restore.sh /var/backups/damson/damson-backup-20261109T020000Z

``damson-backup.sh [-o OUT] [-f PATH]...`` writes a folder
``OUT/damson-backup-<UTC time>/`` with:

- ``database.dump`` (``pg_dump`` custom format; the DSN is a command line
  argument that ``ps`` shows, so keep the password out of it and give it to
  libpq with ``PGPASSWORD`` or ``~/.pgpass``) or ``database.sqlite``
  (``sqlite3 .backup``, consistent while a controller runs);
- ``files.tar.gz`` with each ``-f`` path (configuration, key files), stored
  relative to ``/``. It holds credentials in clear text: it is mode 0600, so
  store or encrypt the folder like a secret;
- ``MANIFEST.sha256``.

The scripts run with umask 077. ``damson-restore.sh BACKUP_DIR`` checks the
manifest (strict), then restores into an **empty** target from the same
variables. On PostgreSQL it runs ``pg_restore --single-transaction``,
all or nothing. On SQLite it refuses to
overwrite an existing file. It never unpacks ``files.tar.gz``; do that yourself
with ``tar -C / -xzf files.tar.gz``.

They do not back up a KEK that lives only in the environment (``DAMSON_KEK``),
the S3 bucket, or the agents. Limits: the dumps are logical, so the RPO is the
age of the backup (use WAL archiving for less); the scripts do not rotate or
ship backups; use the ``pg_dump`` and ``pg_restore`` of the same major version as the server (a newer client fails against an older server).
CI runs them against SQLite and PostgreSQL 18 (``make backup-check``).

A schedule is yours to set up. This is an example, not shipped with Damson:

.. code-block:: ini

   # /etc/systemd/system/damson-backup.service (example)
   [Service]
   Type=oneshot
   EnvironmentFile=/etc/damson/controller.env
   ExecStart=/usr/local/bin/damson-backup.sh -o /var/backups/damson -f /etc/damson

   # /etc/systemd/system/damson-backup.timer (example)
   [Timer]
   OnCalendar=*-*-* 02:00:00
   Persistent=true
   [Install]
   WantedBy=timers.target

or the same command from cron (``0 2 * * *``), plus your own rotation and
copy to another place.

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
