ADR-0070: Prometheus metrics endpoint derived from the database
===============================================================

:Status: Accepted
:Date: 2026-11-09
:Amends: ARCHITECTURE sections 3 and 17 (Prometheus client instead of an OpenTelemetry exporter)

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
``damson_log_object_deletes_pending`` and ``damson_metrics_database_up``. Every
controller therefore reports the same numbers, which suits HA: no controller
holds metric state, and a restart or a leader change loses nothing. Go runtime
and process metrics are those of the scraped controller.

**Authentication.** The endpoint requires the admin token as
``Authorization: Bearer dms_admin...`` and answers 401 otherwise. OIDC sessions
are not accepted. It is not in the OpenAPI spec. It is not unauthenticated
because pool names, queue depths and agent counts reveal the topology and load
of the installation. Prometheus sends the token with ``bearer_token_file``.

**Readiness.** ``/readyz`` also probes external log backends (S3: the bucket
exists), cached for 10 seconds. A failure answers 503 problem+json and the
cause is only logged. The ``db`` log store adds no probe.

Consequences
------------

- Operators can scrape and alert on queue depth, connected agents and capacity
  with stock Prometheus.
- Scrapes cost a few small aggregate queries; scrape one controller or all,
  but do not sum across controllers.
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
