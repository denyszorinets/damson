.. _reference-http-api:

HTTP server and errors
======================

``damson controller`` and ``damson dev`` serve HTTP on ``controller.listen``
(default ``:8080``). The server lives in ``internal/api``. Typed ``/api/v1``
endpoints (Huma, OpenAPI) arrive with later milestones; today the server
provides the endpoints below.

The health and readiness endpoints use plain stdlib ``net/http``. Huma arrives
with the first typed ``/api/v1`` endpoint; at that point ``WriteError`` and
``problemFor`` in ``internal/api`` become Huma's error transformer, so there
remains one error-to-problem+json mapping.

Endpoints
---------

.. list-table::
   :header-rows: 1

   * - Path
     - Behaviour
   * - ``GET /healthz``
     - Liveness. ``200 {"status":"ok"}`` whenever the process serves requests.
       It does not touch the database, so an orchestrator does not restart a
       healthy controller because the database is briefly down.
   * - ``GET /readyz``
     - Readiness. ``200 {"status":"ready"}`` when the database answers
       ``Ping`` and at least one migration is applied; otherwise ``503``
       problem+json. The cause is logged, never returned.
   * - ``/api/...``
     - Any path under ``/api/`` that no route claims returns ``404``
       problem+json (never the web UI).
   * - ``GET /agent/v1/connect``
     - Agent WebSocket gateway (see :ref:`reference-agent-gateway`).
   * - everything else
     - The embedded web UI (see :doc:`/development/web-ui`).

``HEAD`` is accepted on the health endpoints; other methods return ``405``
problem+json with an ``Allow`` header.

.. note::

   The log store check and a "migrations current" comparison against the
   binary's latest migration (ARCHITECTURE section 17) are not implemented
   yet: readiness currently means the database is reachable and migrated at
   least once.

Errors
------

All API errors are RFC 9457 ``application/problem+json``, produced by one
function (``api.WriteError``) that maps store sentinel errors:

.. list-table::
   :header-rows: 1

   * - Error
     - Status
     - ``type``
   * - ``store.ErrNotFound``
     - 404
     - ``urn:damson:problem:not-found``
   * - ``store.ErrConflict``
     - 409
     - ``urn:damson:problem:conflict``
   * - ``store.ErrStale``
     - 409
     - ``urn:damson:problem:stale``
   * - database unavailable
     - 503
     - ``urn:damson:problem:unavailable``
   * - anything else
     - 500
     - ``urn:damson:problem:internal``

Stale (a lost compare-and-swap) is ``409`` rather than ``412`` because clients
do not send ``If-Match`` yet. Server errors carry a generic ``detail``; the real
error is logged with the same ``request_id`` returned in the body and in the
``X-Request-Id`` response header.

Server behaviour
----------------

* Each request logs one JSON line: ``method``, ``path`` (no query string),
  ``status``, ``duration_ms`` and ``request_id``. Headers and bodies are never
  logged.
* Timeouts: header read 10 s, request read 30 s, idle 2 min. There is no write
  timeout because SSE streams will be long-lived.
* On SIGINT or SIGTERM the listener closes and in-flight requests get 15 s to
  finish; then the store is closed.

.. _reference-agent-gateway:

Agent gateway
-------------

``GET /agent/v1/connect`` (package ``internal/controller``) upgrades to the
agent protocol (:doc:`protocol`). Everything is checked **before** the
upgrade, and a failure is a problem+json response with no upgrade:

.. list-table::
   :header-rows: 1

   * - Status
     - Cause
   * - ``403``
     - Plain ``ws://`` without ``controller.insecure_dev`` (no TLS and no
       ``X-Forwarded-Proto: https``).
   * - ``401``
     - ``Authorization: Bearer <pool token>`` missing, unknown or revoked
       (with ``WWW-Authenticate: Bearer``).
   * - ``400``
     - ``X-Damson-Agent-Id`` missing, over 128 bytes or not printable.
   * - ``503``
     - The gateway is stopped or shutting down (also when the database was
       not migrated at start).

The token is hashed (SHA-256) and looked up; it is never logged or stored.
After the upgrade the agent must send ``agent.hello`` within 10 seconds.

Behaviour
~~~~~~~~~

* ``agent.hello``: the controller negotiates the protocol version (N and N-1),
  checks that ``pool`` matches the token's pool, and takes ownership of the
  agent row ordered by ``session_seq`` (a stale ``session_seq`` is refused).
  A newer socket for the same agent supersedes and closes the older one. The
  response carries ``heartbeat_interval_ms`` (10000), the limits and a
  reconciliation list; until dispatch exists (M2) every reported attempt is
  unknown and answered ``cancel``. A refused hello is answered with a
  JSON-RPC error and the socket is closed.
* ``agent.heartbeat``: kept in memory only. Every 10 seconds one batched
  write updates ``last_seen`` and the telemetry snapshot of every agent that
  sent a heartbeat, fenced by controller and ``session_seq``.
* The controller registers itself in ``controller_instances`` at start and
  heartbeats every 5 seconds.
* On graceful stop (after the HTTP listener closed) the controller sends
  ``agent.reconnect`` (jitter up to 5 seconds) to every socket, writes the
  last telemetry batch, closes the sockets and deregisters.
* When an agent socket ends, the agent row is marked disconnected unless a
  newer session already owns it.
