.. _reference-http-api:

HTTP server and errors
======================

``damson controller`` and ``damson dev`` serve HTTP on ``controller.listen``
(default ``:8080``). The server lives in ``internal/api``. The typed
``/api/v1`` endpoints use Huma (ADR-0018) and are listed under
:ref:`reference-api-v1`; the health and readiness endpoints use plain stdlib
``net/http``. Huma's own errors (request validation) and handler errors are
both produced from ``problemFor``'s ``Problem`` type, so there is one
error-to-problem+json mapping.

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
   * - ``/api/v1/...``
     - The typed API (see :ref:`reference-api-v1`).
   * - ``/api/...``
     - Any other path under ``/api/`` that no route claims returns ``404``
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

.. _reference-api-v1:

Typed API (``/api/v1``)
-----------------------

The OpenAPI 3.1 document is served at ``GET /api/v1/openapi.json``. It is
generated from the Go types and committed as ``web/openapi.json``; ``make gen``
rewrites it and regenerates the TypeScript types in
``web/src/lib/api/schema.d.ts`` (``openapi-typescript``). Both are generated:
never edit them. ``web/src/lib/api/client.ts`` wraps the types with
``openapi-fetch``, and the UI calls the API only through it. The CI job
"Generated code is up to date" runs ``make gen`` and fails on any diff, and a
Go test fails when ``web/openapi.json`` differs from the live document.

.. list-table::
   :header-rows: 1

   * - Endpoint
     - Behaviour
   * - ``GET /api/v1/agents``
     - Agents ordered by id, ``items`` plus ``next_cursor``. Each agent has
       ``status`` (``connected`` while a controller owns its session,
       otherwise ``disconnected``), ``pool_id``, ``version``, ``protocol``,
       ``labels``, ``capabilities``, ``capacity``, ``telemetry`` (last
       heartbeat snapshot: optional ``free_slots``, ``cpu_percent``,
       ``memory_bytes``, ``load1``, ``spool_bytes`` and
       ``running_attempts``; missing or malformed fields are omitted), ``connected_at``, ``last_seen`` and ``disconnected_at``.
   * - ``GET /api/v1/agents/{id}``
     - One agent, with its last telemetry. ``404`` if unknown.
   * - ``GET /api/v1/pools``
     - Pools ordered by name, ``items`` plus ``next_cursor``.
   * - ``GET /api/v1/plugins``
     - Every plugin known to this controller, ordered by name, as ``items``
       (not paged). Each plugin has ``name`` and ``versions`` (highest
       semver first). A version has the manifest (``version``, ``title``,
       ``description``, ``input_schema``, ``output_schema``, ``idempotent``,
       ``requires``, ``target_mode``), its ``checksum``, ``builtin``,
       ``pools`` and ``agents`` (the connected agents running it) and
       ``conflicts`` (agents reporting a different manifest for the same
       version; they receive no work). See :ref:`plugin-registry`.
   * - ``GET /api/v1/plugins/{name}``
     - One plugin with all known versions. ``404`` if unknown.
   * - ``POST /api/v1/jobs/validate``
     - Dry-run validation of ``{"spec": {...}}``. Always ``200`` with
       ``valid``, ``errors`` and ``warnings`` (each ``{path, message}`` with a
       spec path such as ``spec.steps[0].with.host``). Errors come from the
       job schema and engine validation, then from checking each plugin
       step's ``with`` against the input schema of the highest known version
       of its plugin (``pluginreg.Lookup``). A plugin no connected agent or
       built-in provides is a **warning**, not an error: its agents may not
       be connected yet, and its input is not checked. A ``with`` value that
       holds a ``${{ }}`` expression is not checked either (it is resolved,
       then validated, at run time). The graph is added in M3.
   * - ``POST /api/v1/jobs``
     - ``{"name"?, "spec"}``. Validates like ``validate`` (errors are a
       ``422`` with a ``location`` per problem; warnings come back in the
       ``201`` body), resolves ``spec.metadata.project`` (``422`` if
       unknown) and creates the job with version 1. ``name`` defaults to
       ``spec.metadata.name`` and must equal it. ``409`` if the name is
       taken in the project. The spec is stored byte for byte; the
       ``checksum`` is the SHA-256 of its canonical form.
   * - ``GET /api/v1/jobs``
     - Jobs of a project (``project`` query, default ``default``), keyset
       paged by id.
   * - ``GET /api/v1/jobs/{id}``
     - The job and its current version including ``spec``.
   * - ``POST /api/v1/jobs/{id}/versions``
     - ``{"spec", "expected_version"?}``. Appends an immutable version. It is a
       compare-and-swap on ``current_version``: a stale ``expected_version``
       is ``409`` (``urn:damson:problem:stale``). Omitting it means "the
       version current when the request is handled". The spec must keep the
       job's project and name (``422`` otherwise).
   * - ``GET /api/v1/jobs/{id}/versions``
     - Versions ascending, without ``spec``, paged.
   * - ``GET /api/v1/jobs/{id}/versions/{v}``
     - One version with its ``spec``.
   * - ``POST /api/v1/jobs/{id}/runs``
     - API trigger, optional body ``{"options"?, "version"?,
       "concurrency_policy"?}`` (see :ref:`reference-trigger`). ``201`` with
       the run and its step tree.
   * - ``GET /api/v1/runs``
     - Runs newest first, paged, filtered by ``job`` (job id) and
       ``status`` (``pending``, ``running``, ``succeeded``, ``failed``,
       ``canceled``). Items have no step tree.
   * - ``GET /api/v1/runs/{id}``
     - The run (``status``, ``trigger``, resolved ``options``, timestamps,
       ``job_version``) with ``steps``: every step instance in creation
       order with its resolved ``input`` (secret references only) and its
       ``executions`` (attempt number, state, pool, agent, outputs, error).
       The attempt id, which fences agent reports, is never exposed.
   * - ``POST /api/v1/runs/{id}/cancel``
     - Records the cancel request (``cancel_requested_at``); the orchestrator
       cancels the run on its next advance. Idempotent: repeating it while
       the run is active is ``200`` with the run. A run that already reached
       a terminal status is ``409`` (``urn:damson:problem:conflict``).
   * - ``GET /api/v1/runs/{id}/events``
     - Server-sent events, see :ref:`reference-sse`.
   * - ``GET /api/v1/executions/{id}``
     - One execution with ``run_id``, ``step_path`` and ``plugin``.
   * - ``GET /api/v1/executions/{id}/logs``
     - Server-sent log stream, see :ref:`reference-sse`.

Lists (except plugins) take ``limit`` (1 to 200, default 50) and ``cursor`` (the previous
page's ``next_cursor``, opaque, empty on the last page). An invalid ``limit`` is a
``422`` problem with an ``errors`` list; a malformed ``cursor`` is a ``400``.

All endpoints are **unauthenticated** for now: authentication and
authorization arrive in M8 (ARCHITECTURE section 12), so anyone who can reach
the listener can create jobs and trigger runs. Pool and pool token
administration stays in the ``damson admin`` CLI until then. No endpoint
returns a pool token or its hash, a secret value or an attempt id.

.. _reference-trigger:

Triggering a run
~~~~~~~~~~~~~~~~

``POST /jobs/{id}/runs`` implements the trigger step of the run lifecycle
(ARCHITECTURE section 8.3):

#. The pinned version is the request's ``version`` or the job's current one.
#. ``options`` get the ``default`` of every property of ``spec.options`` that
   the caller omitted, then are validated against ``spec.options`` (JSON
   Schema, with the same size limits and deny-all URL loader as plugin
   schemas). Failures are a ``422`` listing ``options.<path>`` locations;
   messages never repeat a submitted value. A job without ``spec.options``
   accepts no options.
#. ``concurrency_policy`` accepts ``queue`` (the default) until M3; ``skip``
   and ``cancel_previous`` are a ``422``. ``spec.concurrency`` is still
   rejected at authoring time.
#. Target resolution is a stub until the inventory work.
#. ``engine.Decide`` is called for the queued run. The step instances it
   creates, with resolved input (secret references stay literal) and their
   first executions, are inserted together with the run by one
   ``CreateRun`` transaction. The run is ``pending``; the pool of an
   execution is the step's ``runs_on.pool``, or ``default``.
#. Enqueueing ``run.advance`` in the same transaction arrives with the
   orchestrator (#53); until then nothing advances a pending run.

Options are stored with the run and returned by the API. They must carry
``secret://`` references, never secret values (ARCHITECTURE section 9).

.. _reference-sse:

Server-sent events
~~~~~~~~~~~~~~~~~~

Both streams answer ``text/event-stream`` after the target was checked
(``404`` problem+json otherwise). A comment line (``: heartbeat``) is sent
every 15 s so proxies keep idle streams open. Streams end when the client
disconnects or after the final ``end`` event.

``GET /runs/{id}/events`` sends ``snapshot`` (the full run, as
``GET /runs/{id}``), then ``run`` (``{status, cancel_requested}``) and
``step`` (``{path, lane, status, attempt, execution_id, state}``) whenever
something changed, then ``end``. ``lane`` is the ``for_each`` lane and is
empty before M3; ``attempt`` and ``state`` describe the latest execution. The
server polls the database about once a second and compares states, so a
change is delivered within that interval; a ``RunNotifier`` can later wake the
stream earlier. Events carry numeric ids. The server cannot replay missed
events: on reconnect (``Last-Event-ID``) it sends a fresh ``snapshot`` and
continues the ids from the given value.

``GET /executions/{id}/logs?after_seq=N`` sends, in order: stored chunks
(``log`` events ``{seq, ts, stream, line}``, ``id`` = ``seq``), then the live
tail, then ``end`` (``{state, last_seq}``) once the execution is terminal and
every stored line was sent. ``Last-Event-ID`` takes precedence over
``after_seq`` (browsers resend it on reconnect). A ``gap`` event
``{seq_from, seq_to}`` reports a range that will never arrive: lines the
agent declared lost when its spool overflowed, or a stored chunk whose object
is missing. Streaming continues after a gap. Without a live tail (the
controller's ring buffer, wired by the orchestrator) the stream polls the
store for newly flushed chunks, which arrive up to the chunk flush interval
late.

Errors
------

All API errors are RFC 9457 ``application/problem+json``, produced by one
function (``problemFor``, used by ``api.WriteError`` and by the Huma
handlers) that maps store sentinel errors:

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
   * - invalid request (Huma validation, job spec or run options)
     - 422
     - ``urn:damson:problem:validation``
   * - cancel of a finished run
     - 409
     - ``urn:damson:problem:conflict``
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
  timeout because SSE streams are long-lived.
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
     - Plain ``ws://`` without ``controller.insecure_dev`` (no TLS and, unless
       ``controller.trust_forwarded_proto`` is set, no
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
