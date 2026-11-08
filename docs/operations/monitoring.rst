Monitoring
==========

Damson has no metrics endpoint today. What you can monitor is described here.
:ref:`The plan <operations-monitoring-planned>` is at the end.

Health endpoints
----------------

Both are unauthenticated ``GET`` (or ``HEAD``) requests on ``controller.listen``.

``/healthz``
   Liveness. Answers 200 ``{"status":"ok"}`` whenever the process serves HTTP;
   it checks nothing else. Use it to restart a hung process.

``/readyz``
   Readiness. Answers 200 ``{"status":"ready"}`` when the database answers a
   ping (3 second limit) and migrations have been applied. Otherwise it answers
   503 problem+json; the cause (``database ping``, ``migration version`` or
   ``no migrations applied``) is logged, not returned. It does not check the S3
   log store, OpenBao or the other controllers. Use it for the load balancer and
   for alerting.

The Helm chart wires startup, readiness and liveness probes to these paths. On
VMs, probe them from your load balancer or monitoring system:

.. code-block:: console

   $ curl -fsS -o /dev/null -w '%{http_code}\n' https://damson.example.com/readyz

Logs
----

Controllers and agents write JSON ``slog`` lines to stderr (journald under
systemd). Every line has ``time``, ``level``, ``msg`` and ``role``
(``controller``, ``agent`` or ``dev``). Where relevant it carries ``run_id``,
``execution_id``, ``attempt_id``, ``agent_id``, ``controller_id`` and, for HTTP
requests, ``request_id`` (the same id is in problem+json responses). The level
is ``log.level``. Secret values are never logged (:doc:`hardening`).

Log lines worth alerting on, with the level they have today:

.. list-table::
   :header-rows: 1
   :widths: 14 46 40

   * - Level
     - Message (substring)
     - Meaning
   * - ERROR
     - ``renewing leader lease``, WARN ``leader lease lost``
     - The controller lost contact with the database or the lease. Repeated
       lines mean it is not coordinating. See :doc:`ha`.
   * - ERROR
     - ``controller instance heartbeat failed``
     - Writing to the database fails.
   * - WARN
     - ``execution lost: lease expired``
     - An agent stopped renewing an attempt: the agent or its network is gone.
       Carries ``agent_id`` and ``execution_id``.
   * - WARN
     - ``database is not migrated``
     - A controller runs without ``--auto-migrate`` on an unmigrated database.
       ``/readyz`` is 503.
   * - WARN
     - ``the instance is still encrypted under the previous KEK``
     - A KEK rotation was started and ``damson admin rotate-kek`` has not
       finished (:doc:`upgrade`).
   * - WARN
     - ``secrets are disabled: no KEK configured``
     - Steps that use ``secret://`` refs will fail.
   * - ERROR
     - ``secret is unavailable``, ``secret cannot be decrypted``
     - Dispatch could not read a secret. Carries the ref, never the value.
   * - ERROR
     - ``openbao is unreachable``, ``openbao login failed``
     - The OpenBao backend is down; runs that need secrets fail until it
       answers (:doc:`/reference/secrets-openbao`).
   * - WARN
     - ``deleting log objects``, ``log objects are queued for deletion but no
       external log store is configured``
     - The S3 sweeper cannot delete (bucket down), or a controller lacks the
       ``logs.s3`` block (:doc:`troubleshooting`).
   * - ERROR
     - ``flushing log chunk``
     - Log chunks could not be stored (database or bucket).
   * - WARN
     - ``log stream: chunk unreadable``
     - A stored chunk could not be read: a gap in a log stream.
   * - WARN
     - ``agent.hello rejected``, ``plugin manifests rejected``
     - An agent was refused, with the reason.
   * - WARN
     - ``auth.require_token is off``
     - The job and run API is open (:doc:`hardening`).
   * - WARN
     - ``plugins not offered: the agent lacks the programs they need``
     - Agent log. Jobs using those plugins cannot run on that agent
       (:doc:`troubleshooting`).

Operational API
---------------

- **Agents and pools.** ``GET /api/v1/agents`` lists each agent with its
  ``status`` (``connected`` or ``disconnected``), ``last_seen``, capacity and
  the last heartbeat telemetry (free slots, CPU, memory, load, running
  attempts). ``GET /api/v1/pools`` lists pools. The UI shows the same on the
  **Agents** page (:doc:`/reference/http-api`). A script can alert when a pool
  has no connected agent.
- **Audit log.** ``GET /api/v1/audit`` is the record of who changed what:
  logins, token and secret changes, job changes, triggers, ``secret.kek_rotate``
  and ``audit.prune``. Keep it for as long as your policy requires
  (``audit.retention_days``, default keep forever;
  :doc:`/reference/audit`).
- **Runs.** The run and execution endpoints show queued, running and failed
  work. There is no queue-depth endpoint; a growing number of ``queued`` runs
  for a pool is the signal (:doc:`troubleshooting`).

Alert ideas
-----------

- ``/readyz`` fails for more than a minute on any controller, or fewer than two
  controllers are ready.
- A pool has no ``connected`` agent for five minutes.
- ``execution lost: lease expired`` appears (rate above your baseline).
- ``leader lease lost`` or ``renewing leader lease`` repeats for more than a
  minute.
- Any ``openbao is unreachable`` or ``log objects are queued for deletion``
  line.
- The database: connection count, replication lag, disk space (the ``db`` log
  store grows with log volume), long-running transactions.
- Certificate expiry of the proxy in front of the controllers: agents with an
  expired certificate cannot connect.

.. _operations-monitoring-planned:

What is not there yet
---------------------

ARCHITECTURE section 17 plans OpenTelemetry traces (run, step instance,
execution, with the trace context passed to agents), Prometheus-style metrics
(``damson_runs_total``, ``damson_queue_depth``, ``damson_agents_connected``,
``damson_leases_lost_total`` and others) and a ``/metrics`` endpoint, and a
``/readyz`` that also checks the log store. None of these exist in the code
today: no ``/metrics`` route, no OpenTelemetry instrumentation in Damson's own code. Agent telemetry is
stored as the last snapshot on the agent row only. Use the signals above until
then.
