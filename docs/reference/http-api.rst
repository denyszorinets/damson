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
       then validated, at run time). When the spec is valid the
       body also has ``graph`` (see ``GET /jobs/{id}/versions/{v}/graph``).
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
   * - ``GET /api/v1/jobs/{id}/versions/{v}/graph``
     - The graph of one version, built by ``engine.BuildGraph``: ``nodes``
       (``id``, ``kind`` of ``start``, ``end``, ``step``, ``group``, ``catch``,
       ``finally``, ``parent``, ``label``, ``plugin``, ``when``,
       ``continue_on_error``, ``retry_max_attempts``, ``timeout``, ``rethrow``,
       ``for_each``) and ``edges`` (``from``, ``to``, ``kind`` of ``sequence``,
       ``skip``, ``catch``, ``finally``, ``label``). Ids are step paths;
       children of a ``for_each`` group use ``<group>/for_each[*]/<id>``.
       Nodes carry no step input. ``404`` if unknown; ``500`` if the stored
       spec no longer parses. See ADR-0041.
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
       ``executions`` (attempt number, state, pool, agent, outputs, ``error`` message and
       ``error_kind``).
       The attempt id, which fences agent reports, is never exposed.
   * - ``GET /api/v1/runs/{id}/graph``
     - ``{graph, steps}``: the graph of the run's pinned version and every
       step instance in creation order (``path``, ``node``, ``lane``,
       ``status``, ``attempt``, ``execution_id``, ``state``). Match an
       instance to its box by ``node`` and ``lane``.
   * - ``GET /api/v1/runs/{id}/targets?cursor=&limit=``
     - Needs the admin token (``bearerAuth``). The nodes the run froze at
       trigger, in snapshot order: ``items`` (``name``, ``node_id``, ``pool``,
       ``os``, ``transport``, ``address``, ``tags``, ``attributes``) plus
       ``next_cursor``. Never a credential or host key. ``limit`` defaults to 50,
       max 200. ``404`` for an unknown run, ``400`` for a bad cursor. A run
       without snapshot rows returns empty ``items``.
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

Except the secrets routes (:ref:`reference-secrets-api`) and the nodes routes (:ref:`reference-nodes-api`), all endpoints are **unauthenticated** for now: authentication and
authorization arrive in M8 (ARCHITECTURE section 12), so anyone who can reach
the listener can create jobs and trigger runs. Pool and pool token
administration stays in the ``damson admin`` CLI until then. No endpoint
returns a pool token or its hash, a secret value or an attempt id.

.. _reference-secrets-api:

Secrets API
~~~~~~~~~~~

Secrets routes need ``Authorization: Bearer <admin token>``, the token made by
``damson admin admin-token create`` (:ref:`cli-admin`), except ``GET
/api/v1/secret-types``. A missing or wrong token (including a pool token) is a
``401`` problem with ``WWW-Authenticate: Bearer realm="damson-admin"``, checked
before the body is parsed. The OpenAPI document declares the security scheme
``bearerAuth``. With no KEK configured every secrets route answers ``503`` after
authentication. Values are write-only: no response contains one.

.. list-table::
   :header-rows: 1

   * - Endpoint
     - Behaviour
   * - ``GET /api/v1/secrets?scope=&cursor=&limit=``
     - ``items`` plus ``next_cursor``. Each item is metadata: ``ref``,
       ``scope``, ``name``, ``type``, ``fields`` (field names only),
       ``current_version``, ``created_at`` and ``updated_at``.
   * - ``PUT /api/v1/secrets/{scope}/{name}``
     - Body ``{type, fields: {name: value}, if_version?}``. ``scope`` is one
       percent-encoded path segment: ``infra%2Fbilling`` for scope
       ``infra/billing``, ``_inventory`` for node credentials. Without
       ``if_version`` it creates the secret or adds a version; ``0`` means
       create only; ``n`` must equal ``current_version``. ``201`` on create,
       ``200`` on a new version; the body is the metadata. Errors: ``401``;
       ``409`` (version conflict, already exists with ``if_version`` ``0``, or
       a type change); ``413`` (a field over 64 KiB or the total over 256 KiB);
       ``422`` (bad scope, name or type; a missing, unknown or invalid field,
       with messages naming fields, never values); ``503``.
   * - ``DELETE /api/v1/secrets/{scope}/{name}``
     - ``204``, or ``404`` if unknown.
   * - ``GET /api/v1/secret-types`` (public)
     - ``items`` of ``{name, fields: [{name, required, multiline}]}``.

``PUT`` and ``DELETE`` write an audit event in the same transaction (see
:ref:`reference-audit-api`).

.. _reference-audit-api:

Audit API
~~~~~~~~~

``GET /api/v1/audit?target_type=&target=&cursor=&limit=`` needs the admin
token. It returns ``items`` (newest first; ``id``, ``at``, ``actor``,
``action``, ``target_type``, ``target``, ``project_path``, ``details``,
``request_id``) and ``next_cursor``; ``limit`` is 1 to 200, default 50.
Events never contain secret values. Currently recorded: ``secret.create``,
``secret.update``, ``secret.delete``, ``secret.kek_rotate`` and ``log.purge``. Actors are
``admin-token:<12 hex of the token hash>``, ``system:dev-nodes`` and
``cli:<os user>``. Design: :doc:`/architecture/decisions/0053-audit-events`.

.. _reference-log-purge-api:

Log purge
~~~~~~~~~

``DELETE /api/v1/executions/{id}/logs`` needs the admin token. It removes all
stored log chunks and objects of one terminal execution and writes a
``log.purge`` audit event (target type ``execution``; details ``execution_id``,
``run_id``, ``chunks``, ``objects``; never log content) in the same
transaction. Answers: ``200`` with ``{"chunks": N, "objects": N}``, ``401``,
``404`` for an unknown execution, ``409`` while the execution is not terminal
(cancel it first). Repeating the call is allowed. Limits: the live in-memory
tail and the agent's local spool are not touched; the UI shows no purged marker
(the execution just has no stored log); only the ``db`` log store exists today,
so purge deletes db rows, and future ``fs``/``s3`` stores must implement the
same delete. Design: :doc:`/architecture/decisions/0047-agent-side-masking-only`
and :doc:`/architecture/decisions/0053-audit-events`.

.. _reference-nodes-api:

Nodes API
~~~~~~~~~

Every nodes route, reads included, needs ``Authorization: Bearer <admin token>``
(see the Secrets API above). A missing or a wrong token gets the identical
``401`` text. Design: :doc:`/architecture/decisions/0043-inventory-nodes`.

A node response has every column: ``id``, ``name``, ``address``, ``port``,
``transport``, ``os``, ``pool``, ``tags``, ``attributes``, ``credential`` (a
secret ref only), ``host_key``, ``host_key_policy``, ``source``,
``external_id``, ``enabled``, plus ``host_key_fingerprint`` and
``host_key_observed_fingerprint`` (``SHA256:...``), ``version``, ``created_at``
and ``updated_at``.

.. list-table::
   :header-rows: 1

   * - Endpoint
     - Behaviour
   * - ``GET /api/v1/nodes?source=&enabled=true|false&selector=&cursor=&limit=``
     - ``items`` plus ``next_cursor``. ``selector`` (up to 4096 characters) is a
       CEL selector as in ``spec.targets.selector``, evaluated over the node
       map (``id``, ``name``, ``address``, ``tags``, ``attributes``, ``pool``,
       ``os``, ``transport``) with empty ``inputs``; only matching nodes are
       returned. An invalid selector is a ``422`` at ``query.selector``. At
       most 2000 nodes are scanned per request, so a page can hold fewer than
       ``limit`` matches and still carry ``next_cursor`` (the last name
       scanned); keep following it. ``source`` and ``enabled`` still apply. The
       preview does not apply the project's scope and uses empty ``inputs``.
   * - ``GET /api/v1/nodes/{name}``
     - One node, or ``404``.
   * - ``POST /api/v1/nodes``
     - Creates a node with ``source`` ``manual``. ``name`` and ``address`` are
       required. ``port`` defaults to 22 for ``ssh`` and 5986 for ``winrm``,
       ``transport`` to ``ssh``, ``host_key_policy`` to ``strict`` and
       ``enabled`` to true. ``os``, ``pool``, ``tags``, ``attributes``,
       ``credential`` and ``host_key`` are optional. ``source``,
       ``external_id``, ``version`` and ``host_key_observed`` are not accepted
       (``422``). ``201`` with the node.
   * - ``PATCH /api/v1/nodes/{name}?force=``
     - Every field is optional. ``tags`` and ``attributes`` replace the whole
       value; an empty ``credential`` or ``host_key`` clears it. ``name``,
       ``source`` and ``external_id`` cannot be changed (``422``). The optional
       body field ``if_version`` gives ``409`` if the node moved; without it
       the handler merges against the version it read and retries up to 3
       times. ``200`` with the node.
   * - ``DELETE /api/v1/nodes/{name}?force=&if_version=``
     - ``204``, or ``404``.
   * - ``DELETE /api/v1/nodes/{name}/host-key``
     - Forgets the observed host key, so the next ``tofu`` connection observes
       again. ``204``, or ``404``. Works on synced nodes without ``force``
       (the observed key is not source-owned). A pinned ``host_key`` is not
       touched; ``PATCH`` ``host_key`` pins a key and wins over the observed one.

Errors: ``401``; ``404``; ``409`` (a duplicate name, including a case-only
duplicate; a duplicate ``(source, external_id)``; a stale ``if_version``);
``422`` with a location per field, ``body.<field>``.

Writing a node whose source is not ``manual`` without ``force=true`` is a
``409`` problem of type ``urn:damson:problem:inventory-source-conflict``. With
``force=true`` the write happens but keeps ``source``, so the owning source's
next sync overwrites the edit.

Names match the 63-character lane-key grammar
``^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$``, because the name is a ``for_each`` path
segment. Put an FQDN in ``address``.

.. _reference-inventory-sync:

Inventory sync
~~~~~~~~~~~~~~

External systems push the nodes they own, one source at a time. Damson has no
schedule and no pull: a source's own tooling calls these routes. Both routes
need the admin token (a missing and a wrong token get the same ``401`` as the
other admin routes), accept a body of at most 16 MiB, and take at most 5000
nodes per call. The whole call is one transaction.

``{source}`` is a lowercase name (letters, digits and ``-``, at most 63
characters). ``manual`` and ``devfile`` are reserved (``422``).

.. list-table::
   :header-rows: 1
   :widths: 40 60

   * - Endpoint
     - Behaviour
   * - ``PUT /api/v1/inventory/sources/{source}/nodes?dry_run=&prune=``
     - Full desired state of the source. Body ``{"nodes": [SyncNode]}``. With
       ``prune=true`` the source's nodes missing from the body are deleted.
   * - ``POST /api/v1/inventory/sources/{source}/nodes:batch?dry_run=``
     - Incremental. Body ``{"upserts": [SyncNode], "deletes": [{"external_id"
       | "name"}]}``. It never prunes. A delete matches this source's nodes
       by ``external_id``, else by ``name``; a node that is not there is
       ignored (idempotent), and a delete never reaches another source's
       node. Upserting and deleting the same node in one call is a ``422``.

``SyncNode`` fields: ``name`` (required, lane-key grammar), ``external_id``
(optional, the source's own id, at most 255), ``address`` (required), ``port``
(default 22 for ``ssh``, 5986 for ``winrm``), ``transport`` (``ssh`` default,
or ``winrm``), ``os``, ``pool``, ``tags``, ``attributes``, ``credential`` (a
``secret://`` reference to a whole secret), ``host_key`` (the source owns the
pinned key; the observed key is never touched), ``host_key_policy``
(``strict`` default, ``tofu`` or ``insecure``) and ``enabled`` (default true).

Matching is by ``external_id`` first, else by ``name`` (a node of the source
without an external id is adopted). A changed name for the same
``external_id`` is a rename. Unchanged nodes are not written and keep their
version. Nodes of other sources are never touched. The source owns every field
it sends, so a node sent by name only loses its stored ``external_id``: send
the id every time.

A delete frees the name of the node it removes, so a host can be re-identified
in one batch: delete the old ``external_id`` and upsert the same name with the
new one. A source with more than 5000 nodes cannot use the full-state ``PUT``
(a pruning ``PUT`` would delete the rest): use ``:batch``.

A ``null`` ``nodes`` list is a ``422`` (it must not read as an empty desired
state, which with ``prune`` deletes the whole source), and so is a flag that is
present but empty (``?dry_run``): give ``true`` or ``false``. The body must be
read within 30 seconds.

``200`` returns ``{dry_run, created, updated, deleted, unchanged, conflicts}``;
the first four are lists of node names and ``conflicts`` is always empty here
(a conflict is a ``409``, also in a dry run). With ``dry_run=true`` nothing is written and the same
diff is returned.

Errors:

- ``401``.
- ``409`` of type ``urn:damson:problem:inventory-sync-conflict`` when a desired
  name is held by a node of another source (also case-only), or by an own node
  that stays. This covers name swaps and rename chains: split them into two
  calls. ``errors`` has one entry per conflict with ``location``
  ``node.<name>`` and ``message`` ``held by <source>``. Nothing is written,
  also in a dry run.
- ``409`` (stale) when a node was changed concurrently and 3 re-plans did not
  settle it.
- ``422`` for an invalid node, duplicate names or external ids in the body, a
  reserved source, or an upsert and delete of the same node; the error names
  the node and the field.

Nodes owned by a sync source are edited through the nodes API only with
``force=true``, and the next sync overwrites the edit.

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
   accepts no options. Options marked ``x-damson-secret: true`` accept only
   ``secret://`` references (:ref:`job-spec-secret-options`); unmarked options
   are stored and returned verbatim.
#. ``concurrency_policy`` accepts ``queue`` (the default) until M3; ``skip``
   and ``cancel_previous`` are a ``422``. ``spec.concurrency`` is still
   rejected at authoring time.
#. Target resolution is a stub until the inventory work.
#. ``engine.Decide`` is called for the queued run. The step instances it
   creates, with resolved input (secret references stay literal) and their
   first executions, are inserted together with the run by one
   ``CreateRun`` transaction. The run is ``pending``; the pool of an
   execution is the step's ``runs_on.pool``, or ``default``.
#. The resolved input of each created step instance is validated against the
   input schema of the highest known version of its plugin
   (``pluginreg.Lookup``); a mismatch is a ``422`` at
   ``steps[<path>].input.<field>`` and nothing is inserted. A plugin the
   registry does not know (no agent connected yet) is allowed to queue;
   dispatch validates again against the version an agent will run (#53).
#. ``run.advance`` is enqueued in the same transaction as the run insert
   (:doc:`/development/orchestration`), so a run exists if and only if its
   advance job does. ``POST /runs/{id}/cancel`` enqueues in the same
   transaction as the cancel request. A controller whose database is not
   migrated, or one built without the orchestrator, enqueues nothing.

Contract with the orchestrator (#53). The trigger inserts the first step
instance and its queued execution with the run, but not the ``start_run``
action: the run stays ``pending`` and the first ``run.advance`` applies it.
``run.advance`` builds ``RunState`` from the database, including these rows
(``engine.RunStatusFromStore`` and friends map the stored spelling), and
``engine.Decide`` then emits only ``start_run`` and waits; it does not create
the instance or schedule the execution again (pinned by
``TestDecideAfterTriggerInsert``). The dispatcher must claim only executions
of runs that are neither terminal nor ``cancel_requested``, because a run can
be cancelled before anything advances it.

Status spelling. The store, the API and the agent protocol use ``canceled``
(and ``pending`` for a run that has not started); the engine uses
``cancelled`` (and ``queued``) internally. ``internal/engine/status_map.go`` is
the single mapping.

Options are stored with the run and returned by the API. They must carry
``secret://`` references, never secret values (ARCHITECTURE section 9).

.. _reference-sse:

Server-sent events
~~~~~~~~~~~~~~~~~~

At most ``StreamConfig.MaxStreams`` (default 256) streams are open at once;
further requests get ``503``. Each write has a 10 s deadline, after which the
slow client is disconnected. The server polls a cheap change marker (counts
over indexed rows) with 20% jitter and loads the run tree or the chunk list
only when it moved.

Both streams answer ``text/event-stream`` after the target was checked
(``404`` problem+json otherwise). A comment line (``: heartbeat``) is sent
every 15 s so proxies keep idle streams open. Streams end when the client
disconnects or after the final ``end`` event.

``GET /runs/{id}/events`` sends ``snapshot`` (the full run, as
``GET /runs/{id}``), then ``run`` (``{status, cancel_requested}``) and
``step`` (``{path, node, lane, status, attempt, execution_id, state}``) whenever
something changed, then ``end``. ``node`` is the graph node of the instance
(the template id inside a ``for_each``) and ``lane`` the ``for_each`` lane key,
empty outside a ``for_each``; ``attempt`` and ``state`` describe the latest execution. The
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
is missing. Streaming continues after a gap. A gap is reported only when it
is confirmed: the execution is terminal, or the same hole was seen on two
consecutive polls. Chunks can commit out of order, so a hole that fills in
between is never reported and valid lines are never called lost. A stored
chunk whose object is missing is reported at once. Without a live tail (the
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

Internal controller endpoint
----------------------------

Not part of the public API and not in the OpenAPI document. Controllers call
``GET /internal/v1/executions/{id}/tail?attempt_id=&after_seq=`` on each
other's internal listener (``controller.internal_listen``, default ``:8081``),
which exists only when a cluster secret is set. The public listener answers
``404`` for ``/internal/``. Requests carry ``X-Damson-Timestamp``,
``X-Damson-Caller`` and ``X-Damson-Signature`` (HMAC-SHA256, 30 s window). The
response is ``application/x-ndjson`` with ``line`` and ``heartbeat`` objects.
Errors: bare ``401``, ``404`` (no buffer), ``409`` (other attempt), ``503``
(too many tails). The transport is plain HTTP; see
:doc:`/architecture/decisions/0040-internal-controller-endpoints`.

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
  reconciliation list: a ``finished`` attempt that is
  still the current attempt of an execution owned by the agent is answered
  ``continue`` (the agent redelivers ``step.complete``); a running attempt of
  the agent is adopted by this controller (``controller_id`` and lease move,
  fenced on ``session_seq``) and answered ``continue``; everything else is
  answered ``cancel``. A store failure fails the hello, so the agent
  reconnects instead of losing steps. A refused hello is answered with a
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
