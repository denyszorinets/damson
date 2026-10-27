.. _reference-http-api:

HTTP server and errors
======================

``damson controller`` and ``damson dev`` serve HTTP on ``controller.listen``
(default ``:8080``). The server lives in ``internal/api``. Typed ``/api/v1``
endpoints (Huma, OpenAPI) arrive with later milestones; today the server
provides the endpoints below.

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
