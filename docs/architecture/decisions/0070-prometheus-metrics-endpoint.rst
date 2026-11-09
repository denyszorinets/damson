ADR-0070: Prometheus metrics endpoint derived from the database
===============================================================

:Status: Accepted
:Date: 2026-11-09
:Amends: ARCHITECTURE sections 3 and 17 (Prometheus client instead of an OpenTelemetry exporter;
         ``/readyz`` does not probe the log store, ``damson_log_store_up`` does)

Issue :issue:`230`. Related: ADR-0063 (admin token and sessions),
:doc:`0067-s3-log-store-and-deferred-deletes`.

Context
-------

ARCHITECTURE sections 3 and 17 planned OpenTelemetry traces and metrics, with a
Prometheus endpoint through the OTel exporter. Operators had no metrics at all
(``docs/operations/monitoring.rst``). Controllers keep no durable state in
memory, and several run side by side, so a counter held in one process would
show only that controller's share of the work.

Decision
--------

**Library.** ``GET /metrics`` on the controller's main listener, in Prometheus
text format, built directly on ``prometheus/client_golang`` (Apache-2.0). The
OTel SDK is not added: it is larger and the exposition is the same.

**Cluster-wide gauges come from the database.** At scrape time the controller
queries the database and reports ``damson_runs{status}``,
``damson_queue_depth{pool}``, ``damson_executions_active{pool,state}``,
``damson_agents_connected{pool}``, ``damson_agent_slots{pool}``,
``damson_log_object_deletes_pending``, ``damson_log_store_up`` and
``damson_metrics_database_up``. Agents count only when a controller owns them,
they are not disconnected and they were seen in the last 2 minutes, so the
agents of a crashed controller age out. Every
controller therefore reports the same numbers, which suits HA: no controller
holds metric state, and a restart or a leader change loses nothing. Go runtime
and process metrics are those of the scraped controller.

**Authentication.** The endpoint requires a dedicated read-only metrics token
as ``Authorization: Bearer dmm_...``. ``damson admin metrics-token create``
prints it once and replaces any existing one; only its SHA-256 is stored, in
the ``instance_settings`` key ``auth.metrics_token_hash`` (no schema change).
``damson admin metrics-token revoke`` removes it. The token authorizes
``/metrics`` only: the admin token (``dma_``) and service tokens (``dms_``) get
401 there, and the metrics token gets 401 on the API. A request without a
``dmm_`` token gets 401 problem+json with ``WWW-Authenticate: Bearer
realm="damson-metrics"`` before any database access. Create and revoke are
audited as ``metrics_token.create`` and ``metrics_token.revoke`` (target
``metrics_token``, no details). OIDC sessions are not accepted. The endpoint is
not in the OpenAPI spec. It is not unauthenticated because pool names, queue
depths and agent counts reveal the topology and load of the installation. The
admin token is not used because it is the all-powerful recovery token
(ARCHITECTURE section 12.2) and must not live in the monitoring system's
configuration.

**Limits.** ``/metrics`` allows 2 requests in flight and times out after 5
seconds. The gauges are read from the database on each scrape, so the cost
grows with the number of runs.

**Readiness is unchanged.** ``/readyz`` checks the database ping and the
migrations only. It does not probe the log store. External log backends (S3)
are probed instead for ``damson_log_store_up`` (1 or 0; the result is cached
for 10 seconds, the probe has its own 3 second timeout; the metric is absent
with the ``db`` log store). An S3 outage is a degraded state: log flushes retry
(:doc:`0067-s3-log-store-and-deferred-deletes`). Making every controller
unready would turn it into a full outage, because all pods would leave the
Service. Alert on ``damson_log_store_up == 0``. This amends ARCHITECTURE
section 17, which listed "log store reachable" under ``/readyz``.

Consequences
------------

- Operators can scrape and alert on queue depth, connected agents and capacity
  with stock Prometheus.
- Scrapes cost aggregate queries that grow with the number of runs. Scrape
  every 30 to 60 seconds. One controller is enough, since all report the same
  cluster-wide numbers; do not sum across controllers.
- Prometheus needs its own token, created and rotated with the CLI.
- ``damson_runs`` is a gauge by status and drops when runs are removed; it is
  not a counter.
- Only capacity is exported, not free slots.
- Not implemented and still planned: OpenTelemetry traces,
  ``damson_step_duration_seconds``, ``damson_log_bytes_total``,
  ``damson_ws_messages_total``, ``damson_leases_lost_total`` and
  ``damson_runs_total`` as a counter. OpenTelemetry remains the plan for
  traces; metrics may move to it later without changing the names.

Validation
----------

``internal/api/metrics_test.go`` and the store metrics tests on SQLite and
Postgres (``internal/store/storetest/metrics.go``).

References
----------

- :doc:`/operations/monitoring`
- ARCHITECTURE section 17
