ADR-0014: Compiled-in plugins with a public SDK
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Every step calls a plugin (SSH, WinRM, transfer, Ansible, Buildah, Helm, and
third-party ones). Plugins need typed inputs and outputs to drive validation
and UI forms. Third parties must be able to add plugins. Runtime loading
mechanisms have significant costs (isolation, build lock-step, cross-language
boilerplate).

Decision
--------

- ``pkg/pluginsdk`` is a **public, semver-versioned SDK**. Built-in plugins use
  exactly the same SDK as third-party ones.
- Inputs and outputs are Go structs with JSON and jsonschema tags; schemas are
  generated (``invopop/jsonschema``) and validated
  (``santhosh-tekuri/jsonschema/v6``).
- In v1, plugins are **compiled in**. Each package exports
  ``Plugins() []pluginsdk.Plugin``; ``plugins/all`` lists built-ins explicitly;
  no ``init()`` registration; no package-level mutable state.
- Out-of-tree plugins are added with ``damson build --with <module>@<version>``,
  which builds a custom binary (like ``xcaddy``).
- Agents send full plugin manifests in ``agent.hello``; the controller stores
  them and dispatches only to agents with a compatible version.
- The core reaches plugins only through the registry interface.
- ``plugintest`` provides a contract suite (schema generation, cancellation,
  secret masking, output conformance) that every plugin passes.

Alternatives Considered
-----------------------

Out-of-process plugins over gRPC (hashicorp/go-plugin)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- gRPC boilerplate in every plugin for isolation and multi-language support
  that are not needed.

Executable plugins (JSON over stdin/stdout)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Only valuable for plugins in other languages, which are not needed.

Runtime-loaded Go ``.so`` plugins
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Deferred; constraints recorded in :doc:`0032-runtime-loaded-plugins`.

Decision Criteria
-----------------

Simplicity, type safety, schema generation, third-party extensibility,
operational risk.

Rationale
---------

Compiling plugins in is the simplest model that still supports third parties.
Using the public SDK for built-ins keeps it honest and well exercised, and the
``Plugins()`` export keeps a ``.so`` path open.

Consequences
------------

Positive
~~~~~~~~

- No IPC layer; plugins are plain Go; schemas never hand-written.

Negative
~~~~~~~~

- Adding a third-party plugin needs a custom binary build.
- No process isolation: a plugin panic is recovered per execution, but a hard
  crash takes the agent down.

Risks
~~~~~

- SDK breaking changes. Mitigation: semver, ``plugintest`` and
  ``examples/plugins/hello`` updated in the same change.

Follow-up
~~~~~~~~~

- M0: SDK v0 and ``plugintest``; M5: ``damson build --with`` and plugin tooling.

Validation
----------

``make plugin-contract`` over all built-ins; CI builds ``examples/plugins/hello``
into a custom binary.

References
----------

- :doc:`/architecture/design` §6, §23
