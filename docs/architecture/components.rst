Components
==========

Damson is one Go binary (``cmd/damson``) with controller and agent roles
(:doc:`decisions/0002-single-go-binary-with-controller-and-agent-roles`). This
page maps the planned packages (:doc:`design` §19) to responsibilities and
states the dependency rules between them.

.. note::

   The repository is at milestone M0 (skeleton). Packages below are the
   planned layout, not yet existing code.

Layers and the dependency rule
------------------------------

Dependencies point inward, toward the pure engine.

.. mermaid::

   flowchart BT
     subgraph core["Core (pure, no I/O)"]
       engine["internal/engine<br/>Decide, BuildGraph, spec model"]
       expr["internal/expr<br/>CEL environments"]
     end
     subgraph ports["Ports (interfaces at the consumer)"]
       storei["Store"]
       secreti["SecretStore"]
       logi["LogStore"]
       registry["plugin registry"]
     end
     subgraph adapters["Adapters"]
       store["internal/store<br/>postgres, sqlite"]
       secrets["internal/secrets<br/>db, openbao, masking"]
       logstore["internal/logstore<br/>db, fs, s3"]
       protocol["internal/protocol<br/>JSON-RPC + MessagePack"]
     end
     subgraph entry["Entry points"]
       api["internal/api<br/>Huma, problem+json"]
       controller["internal/controller<br/>dispatch, duties"]
       agent["internal/agent<br/>executor, batcher, masker"]
       cmd["cmd/damson"]
     end
     sdk["pkg/pluginsdk<br/>(public API)"]
     plugins["plugins/*"]

     engine --> expr
     controller --> engine & storei & secreti & logi & registry & protocol
     api --> controller & storei
     store -.implements.-> storei
     secrets -.implements.-> secreti
     logstore -.implements.-> logi
     agent --> protocol & sdk
     plugins --> sdk
     registry --> sdk
     cmd --> api & controller & agent

Rules enforced by review (``CLAUDE.md`` hard rules):

- ``internal/engine`` performs no I/O, never calls ``time.Now()`` and uses no
  randomness; time and randomness are injected. All control-flow semantics live
  there (:doc:`decisions/0009-pure-deterministic-engine`).
- Plugins import only ``pkg/pluginsdk`` and ``plugins/internal/*``, never
  controller or agent internals.
- The core reaches plugins only through the registry interface, never assuming
  a plugin is in-process.
- ``pkg/pluginsdk`` depends only on the standard library and the JSON Schema
  package (:doc:`decisions/0014-compiled-in-plugins-with-a-public-sdk`).
- Interfaces are declared by their consumer. Multiple implementations exist
  where a backend choice is documented: store, secrets, logstore.

Package responsibilities
------------------------

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - Package
     - Responsibility
   * - ``cmd/damson``
     - Subcommands: ``controller``, ``agent``, ``dev``, ``migrate``, ``apply``,
       ``export``, ``admin``, ``plugin``, ``build``.
   * - ``internal/config``
     - Configuration from file, environment and flags (koanf).
   * - ``internal/controller``
     - Wiring, HTTP server, dispatch loop, singleton duties (scheduler, reaper,
       retention), internal endpoints, controller registry.
   * - ``internal/agent``
     - Connection management and reconnect, executor, log batcher, spool,
       masker.
   * - ``internal/protocol``
     - JSON-RPC 2.0 over MessagePack framing, message types, versioning
       (N and N-1).
   * - ``internal/engine``
     - Pure ``Decide()`` and ``BuildGraph()``, spec model, spec validation.
   * - ``internal/expr``
     - CEL environments, ``${{ }}`` interpolation, cost limits.
   * - ``internal/store``
     - ``Store`` interface, Postgres and SQLite implementations, sqlc queries
       and goose migrations per dialect.
   * - ``internal/secrets``
     - ``SecretStore`` interface, ``db`` and ``openbao`` backends, masking.
   * - ``internal/logstore``
     - ``LogStore`` backends ``db``, ``fs``, ``s3``.
   * - ``internal/auth``
     - OIDC, sessions, tokens, CEL policy.
   * - ``internal/inventory``, ``internal/scheduler``
     - Node inventory and source sync; cron scheduling duty.
   * - ``internal/api``
     - Huma handlers; the single error → problem+json mapping.
   * - ``internal/telemetry``
     - OpenTelemetry traces and metrics, ``slog`` setup.
   * - ``pkg/pluginsdk``, ``plugintest``
     - Public plugin SDK and its test harness and contract suite.
   * - ``plugins/*``, ``plugins/all``
     - Built-in plugins and their explicit registration list.
   * - ``web/``
     - SvelteKit SPA embedded in the binary.

Runtime components
------------------

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - Component
     - Behaviour
   * - Dispatch loop (every controller)
     - Wakes on a notify hint or every 1 s; claims due executions for its own
       connected agents with ``SKIP LOCKED``; resolves secrets; sends
       ``step.execute``.
   * - ``run.advance`` (River, any controller)
     - Loads run state, calls ``Decide``, applies actions in one transaction.
   * - Singleton duties (leader lease)
     - Scheduler, reaper, retention. Idempotent.
   * - Log ingest (owning controller)
     - Ring buffer for live tail, chunk writer to the LogStore, ``log.ack``.

Components own their goroutines through ``Start(ctx)`` and ``Stop(ctx)``.
