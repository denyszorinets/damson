Upgrade and rotate
==================

Upgrading means replacing the binary or image of the controllers and then of
the agents. The target guides have the exact commands
(:ref:`Helm <reference-helm>`, :ref:`systemd <reference-systemd>`); this page
explains what happens and what to check.

Before you start
----------------

#. Take a database backup (:doc:`backup`). Migrations run at controller start
   and are forward only.
#. Keep the KEK, the cluster secret and the configuration of the running
   version.
#. Read the release notes for migrations and configuration changes. There is
   no release yet; until there is, treat every build as one that may add
   migrations.

How migrations run
------------------

- A controller started with ``--auto-migrate`` applies pending migrations at
  start, before it opens its listener. The Helm chart and the systemd unit both
  pass the flag. Without the flag a controller on an unmigrated database serves
  ``/healthz`` but answers ``/readyz`` with 503 and runs neither the agent
  gateway nor ``run.advance``; it logs ``database is not migrated; agent
  gateway and run.advance disabled (run with --auto-migrate)``.
- On PostgreSQL the migration holds a session advisory lock. Controllers that
  start together apply the migrations once and the others wait. River's own
  migrations run after Damson's, under a second lock.
- There is no separate ``damson migrate`` command. To migrate without serving
  traffic, start one controller with ``--auto-migrate`` and keep it out of the
  load balancer until ``/readyz`` answers 200.
- The migration files carry ``Down`` sections, but no command runs them. Do not
  rely on them in production (:doc:`/development/store`).

Rolling controllers
-------------------

Upgrade one controller at a time and wait for ``/readyz`` before the next:

#. Replace the binary or image on one controller and restart it. It shuts down
   gracefully: it stops accepting work, tells its agents to reconnect to
   another controller, flushes logs and deregisters. Running steps continue on
   the agents and re-attach to another controller.
#. The new controller migrates, then becomes ready.
#. Continue with the next controller.

While the roll is in progress, old and new controllers share one database.
That is safe only if the migration is compatible with the old code (add
columns and tables first, remove or change them in a later release: expand and
contract). Nothing checks this for you, and ``/readyz`` only proves that the
migrations ran. An incompatible migration makes old controllers return errors
or become briefly unavailable. If a release needs that, stop all controllers,
upgrade, and start them again.

Controllers and agents may differ by one protocol version (N and N-1,
:doc:`/reference/protocol`). An agent whose protocol the controller does not
support is refused and exits with status 1.

Upgrading agents
----------------

Agents have no drain on SIGTERM. Stopping or restarting one cancels its running
steps after a 5 second grace, whether the cause is an upgrade, a node drain or
an autoscaler scale-down. Upgrade a pool when its steps are idle, or accept the
cancellations; a cancelled step is retried only if the job's retry policy says
so. The agent understands the controller message ``agent.drain`` (stop taking new
work), but no controller sends it today. Upgrade pool by pool and keep the other pools
serving.

Rollback
--------

Because migrations are forward only, a rollback after a migration means
restoring the database backup taken before the upgrade, then starting the old
version. Everything written since the backup is lost, including runs and
logs. If the new version did not migrate (the release had no new migration),
you can simply redeploy the old binary. Test the restore path
(:doc:`backup`) before you need it.

Rotate the KEK
--------------

Use this when the key may have leaked or on a schedule. The full command
description is :ref:`cli-rotate-kek`; the steps are:

#. Generate the new key: ``damson admin kek generate``.
#. Roll every controller with the new key in ``DAMSON_KEK`` and the old one in
   ``DAMSON_KEK_PREVIOUS``. They start and log a warning (``the instance is
   still encrypted under the previous KEK; run `damson admin rotate-kek```).
#. Make sure no controller still runs with the old key alone.
#. Run ``damson admin rotate-kek`` with the same environment. It re-wraps every
   stored data key, moves the canary last and can be run again after a crash.
   It writes a ``secret.kek_rotate`` audit event.
#. When it reports the canary moved and ``stragglers`` 0, remove
   ``DAMSON_KEK_PREVIOUS`` and restart the controllers.
#. Store the new key in your escrow (:doc:`backup`). Keep the old key until
   the last database backup that needs it has expired: a restored backup
   from before the rotation is encrypted under the old key.

A controller refuses to start with a key that matches neither the current nor
the previous one (:doc:`troubleshooting`).

Rotate the cluster secret
-------------------------

Two rolling restarts, so controllers never disagree about what they accept. The
steps are in :ref:`the configuration reference <reference-config>` under
"Rotating the cluster secret": set ``cluster_secret = A`` and
``cluster_secret_previous = B`` everywhere, then ``cluster_secret = B`` and
``cluster_secret_previous = A``, then remove the previous value. A failed
request between controllers only costs the live log tail, never data.

Rotate tokens
-------------

.. list-table::
   :header-rows: 1
   :widths: 25 75

   * - Token
     - How
   * - Pool token
     - ``damson admin token create --pool NAME --name NEW``; put it in the
       agents of the pool and restart them; then ``damson admin token revoke
       ID`` for the old one (``token list --pool NAME`` shows the ids). A
       revoked token cannot connect, and an agent that is refused for it exits
       with status 1.
   * - Admin token
     - ``damson admin admin-token create`` replaces the old token at once.
   * - Service account token
     - Create a new token for the account, switch the automation to it, then
       ``DELETE /api/v1/tokens/{id}`` for the old one
       (:doc:`/reference/service-accounts`). Tokens can have an
       ``expires_at``.
   * - OIDC client secret
     - Change it at the provider and in ``DAMSON_AUTH__OIDC__CLIENT_SECRET``,
       then restart the controllers. Sessions last 12 hours and survive the
       restart because they are stored in the database.
   * - Database password
     - Change it in the database and in the DSN, then restart the controllers
       one at a time.

After you change a Kubernetes Secret, restart the Deployments: the chart does
not see the content of a Secret (``kubectl rollout restart``).
