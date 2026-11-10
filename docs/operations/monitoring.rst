Monitoring
==========

Controllers expose Prometheus metrics on ``/metrics``, plus health endpoints,
JSON logs and the operational API. :ref:`What is still missing
<operations-monitoring-planned>` is at the end.

Health endpoints
----------------

Both are unauthenticated ``GET`` (or ``HEAD``) requests on ``controller.listen``.

``/healthz``
   Liveness. Answers 200 ``{"status":"ok"}`` whenever the process serves HTTP;
   it checks nothing else. Use it to restart a hung process.

``/readyz``
   Readiness. Answers 200 ``{"status":"ready"}`` when the database answers a
   ping (3 second limit) and migrations have been applied. Otherwise it
   answers 503 problem+json; the cause (``database ping``, ``migration
   version`` or ``no migrations applied``) is logged, not returned. It does
   not check the log store, OpenBao or the other controllers. Use it for the
   load balancer and for alerting. A log store outage is degraded, not down, so
   it is reported by ``damson_log_store_up`` (see below).

The Helm chart wires startup, readiness and liveness probes to these paths. On
VMs, probe them from your load balancer or monitoring system:

.. code-block:: console

   $ curl -fsS -o /dev/null -w '%{http_code}\n' https://damson.example.com/readyz

Metrics
-------

``GET /metrics`` on ``controller.listen`` serves Prometheus text format. It
requires a dedicated read-only metrics token as ``Authorization: Bearer
dmm_...``. Create it once; it is printed once, replaces any earlier metrics
token, and only its SHA-256 is stored:

.. code-block:: console

   $ damson admin metrics-token create > /etc/prometheus/damson-metrics-token
   $ damson admin metrics-token revoke

The token reads ``/metrics`` and nothing else: the admin token and service
tokens get 401 there, and the metrics token gets 401 on the API. Requests
without a ``dmm_`` token get 401 problem+json before the database is touched.
OIDC sessions are not accepted. Create and revoke are audited
(``metrics_token.create``, ``metrics_token.revoke``). The admin token is
deliberately not used, so the recovery token never sits in the monitoring
system. The endpoint is not in the OpenAPI spec (ADR-0070).

.. code-block:: yaml

   scrape_configs:
     - job_name: damson
       scheme: https
       metrics_path: /metrics
       scrape_interval: 30s
       authorization:
         type: Bearer
         credentials_file: /etc/prometheus/damson-metrics-token
       static_configs:
         - targets: [damson.example.com]

(``bearer_token_file`` in older Prometheus versions.) The workload gauges are
read from the database at scrape time, so every controller reports the same
cluster-wide numbers. Scrape one controller or all of them, but do not sum
across controllers. The gauges are queried on each scrape and the cost grows
with the number of runs, so scrape every 30 to 60 seconds; one controller is
enough. ``/metrics`` allows 2 requests in flight and times out after 5
seconds. The Go (``go_*``) and process (``process_*``) metrics belong to the
scraped controller.

.. list-table::
   :header-rows: 1
   :widths: 38 62

   * - Metric
     - Meaning
   * - ``damson_runs{status}``
     - Runs by status. A gauge: it drops when runs are removed.
   * - ``damson_queue_depth{pool}``
     - Executions in state ``queued`` per pool.
   * - ``damson_executions_active{pool,state}``
     - Executions in state ``dispatched`` or ``running`` per pool.
   * - ``damson_agents_connected{pool}``
     - Agents per pool that a controller owns, that are not disconnected and
       that were seen in the last 2 minutes (a crashed controller's agents age
       out).
   * - ``damson_agent_slots{pool}``
     - Total capacity of those agents of a pool (not free slots).
   * - ``damson_log_object_deletes_pending``
     - Log objects queued for deletion in the external log store.
   * - ``damson_log_store_up``
     - 1, or 0 when an external log backend (S3) fails a probe (cached 10
       seconds, own 3 second timeout). Absent with the ``db`` log store.
   * - ``damson_metrics_database_up``
     - 1, or 0 when the scrape's database query failed.

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
  work. The queue depth per pool is ``damson_queue_depth``; a growing value is
  the signal (:doc:`troubleshooting`).

Alert ideas
-----------

- ``/readyz`` fails for more than a minute on any controller, or fewer than two
  controllers are ready.
- ``damson_log_store_up == 0`` (the log store is down; log flushes retry, runs
  continue).
- ``damson_agents_connected{pool} == 0`` for five minutes.
- ``damson_queue_depth{pool} > 0`` and not falling for ten minutes, or
  ``damson_executions_active{state="running"}`` close to
  ``damson_agent_slots`` (the pool is saturated).
- ``damson_metrics_database_up == 0``, or ``absent(damson_agents_connected)``
  (the scrape fails).
- ``damson_log_object_deletes_pending`` keeps growing (the S3 sweeper cannot
  delete).
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

ARCHITECTURE section 17 still plans OpenTelemetry traces (run, step instance,
execution, with the trace context passed to agents); there is no OpenTelemetry
instrumentation in Damson's own code. These metrics are not exported yet:
``damson_step_duration_seconds``, ``damson_log_bytes_total``,
``damson_ws_messages_total``, ``damson_leases_lost_total`` and
``damson_runs_total`` as a counter (``damson_runs`` is a gauge). There are no
free-slot counts, only capacity. Agent telemetry is stored as the last snapshot
on the agent row only. Use the log lines above for lost leases.
