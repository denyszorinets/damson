Plugin SDK
==========

``pkg/pluginsdk`` is the public SDK every Damson plugin is written against.
Built-in plugins use exactly the same API as third-party ones. The SDK follows
semver (``pluginsdk.Version``). It is **0.x**: the API is treated as stable,
but a breaking change is still possible before 1.0 and is announced in the
changelog. It depends only on the standard library and
``github.com/invopop/jsonschema`` (MIT) for schema generation. The design is in
:doc:`/architecture/design` (ARCHITECTURE section 6).

Writing a plugin
----------------

A plugin package exports one constructor and implements ``pluginsdk.Plugin``:

.. code-block:: go

   func Plugins() []pluginsdk.Plugin { return []pluginsdk.Plugin{plugin{}} }

   type Plugin interface {
       Manifest() Manifest
       Validate(ctx context.Context, input json.RawMessage) error
       Execute(ctx context.Context, req ExecRequest, emit Emitter) (Result, error)
   }

``examples/plugins/hello`` is a complete minimal plugin. The conventions are:

- **Input and output are Go structs** with ``json`` and ``jsonschema`` tags.
  Generate the manifest schemas with ``pluginsdk.MustSchemaFor[Input]()``;
  never write schemas by hand.
- **No ``init()`` registration and no package-level mutable state.** Built-ins
  are listed explicitly in ``plugins/all``. Package-level ``errors.New``
  sentinels and ``regexp.MustCompile`` values are allowed.
- **Imports:** only ``pkg/pluginsdk`` and ``plugins/internal/*`` from Damson.
- **Cancellation:** honor ``ctx`` and stop (and kill remote processes) promptly.
- **Secrets:** resolve ``secret://`` refs with ``req.Secrets`` and never log
  them or put them in outputs or error messages. The agent emitter also masks
  them, but a plugin must not rely on that. ``Resolve(ref)`` returns one value;
  ``ResolveFields(ref)`` returns every field of a whole credential (an
  ``ssh_key``, an ``s3`` connection; ARCHITECTURE sections 9.1 and 9.3). Neither
  takes a ``context.Context`` because the bundle is already in agent memory.
- **Errors:** report step failures as ``Result.Error`` using a typed
  ``*pluginsdk.StepError`` (``connection``, ``auth``, ``timeout``,
  ``exit_code``, ``validation``, ``cancelled``, ``internal``). Return a non-nil
  ``error`` only for unexpected plugin faults; ``pluginsdk.AsStepError`` maps
  any error (including context errors) to a ``StepError``. If ``Execute``
  returns a non-nil error while its context is cancelled, the kind is
  ``cancelled``. The agent recovers a panic in a plugin and reports it as
  ``internal``.
- **Nodes:** ``ExecRequest.Nodes`` holds ``NodeInfo`` values named after the
  inventory node fields (``transport``, ``os``, ``attributes`` as
  ``map[string]any``, ``credential`` ref, ``host_key``, ``host_key_policy``).
- ``Validate`` failures must be ``validation`` errors; they are never retried.

Manifest
--------

``Manifest`` carries the name (``[a-z][a-z0-9_-]*``), semver version, title,
generated input and output schemas, ``Idempotent``, ``Requires`` capabilities
(``bin:git`` style) and ``TargetMode`` (``none``, ``single_node``,
``inventory``). ``Manifest.Validate`` checks it and ``Manifest.Checksum`` gives
the digest the controller uses to detect schema conflicts between agents. The
checksum is computed over the canonical JSON (decoded to generic values and
re-marshaled with sorted keys), so it does not depend on key order or on how the
schema library marshals. Schemas come from ``github.com/invopop/jsonschema``,
pinned in ``go.mod``; a golden test pins the generated output and checksum, so
a version bump that changes schemas is deliberate.

Schema conventions: a field is **required unless its ``json`` tag has
``omitempty``**; objects get ``additionalProperties: false``. ``InputSchema``
validates the **resolved** input, after ``${{ ... }}`` expressions have been
evaluated, not the saved job spec (which may hold expressions in any field).

Contract suite (``plugintest``)
-------------------------------

``pkg/pluginsdk/plugintest`` provides a fake emitter, fake secrets, an ``Exec``
helper (temp ``WorkDir``) and the contract suite every plugin must pass:

.. code-block:: go

   func TestContract(t *testing.T) {
       plugintest.RunContract(t, myplugin.Plugins()[0], plugintest.Options{
           Cases: []plugintest.Case{{Name: "basic", Input: json.RawMessage(`{}`)}},
       })
   }

   func TestSource(t *testing.T) { plugintest.CheckSource(t, ".") }

``RunContract`` checks that:

- the manifest is valid and its schemas marshal;
- ``Validate`` rejects malformed input with a ``validation`` error, and every
  case input conforms to ``InputSchema``;
- ``Execute`` succeeds for every case, and its outputs are a JSON object that
  conforms to ``OutputSchema`` (validated with
  ``github.com/santhosh-tekuri/jsonschema/v6``, Apache-2.0, a dependency of
  ``plugintest`` only; the ``pluginsdk`` package does not import it);
- no secret value from ``Case.Secrets`` or ``Case.Bundle`` leaks into logs,
  progress, logger output, outputs or errors. The fake emitter does not mask, so
  leaks are visible. The check is **plaintext only**; base64, URL-encoded and
  per-line variants are the agent masker's job (ARCHITECTURE section 9.4);
- ``Execute`` returns promptly with a ``cancelled`` error for an
  already-cancelled context and, when ``Options.Blocking`` is set to an
  execution that runs until cancelled, for a running one that is cancelled
  after a short delay. A plugin that ignores cancellation fails after
  ``Options.Timeout``.

``plugintest.Exec`` recovers panics (reported as an ``internal`` error) and
records ``req.Logger`` output in ``Outcome.LogOutput``. ``FakeSecrets`` serves
single values; ``FakeBundle`` serves whole credentials for ``ResolveFields``.

``CheckSource`` statically checks the ``Plugins()`` export, the absence of
``init()`` and mutable package state, and the import rules. Pass
``plugintest.SkipImportRule()`` for ``plugins/all`` (it must import every
built-in). Packages under ``plugins/internal/`` are helpers and are not
required to export ``Plugins()``.

Running the suite
-----------------

.. code-block:: console

   $ make plugin-contract

runs the SDK tests, the contract suite over every built-in plugin
(``plugins/all``), and the ``examples/plugins/hello`` tests and build.
