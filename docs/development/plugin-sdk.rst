Plugin SDK
==========

``pkg/pluginsdk`` is the public SDK every Damson plugin is written against.
Built-in plugins use exactly the same API as third-party ones. The SDK follows
semver (``pluginsdk.Version``) and depends only on the standard library and
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
  them, but a plugin must not rely on that.
- **Errors:** report step failures as ``Result.Error`` using a typed
  ``*pluginsdk.StepError`` (``connection``, ``auth``, ``timeout``,
  ``exit_code``, ``validation``, ``cancelled``, ``internal``). Return a non-nil
  ``error`` only for unexpected plugin faults; ``pluginsdk.AsStepError`` maps
  any error (including context errors) to a ``StepError``.
- ``Validate`` failures must be ``validation`` errors; they are never retried.

Manifest
--------

``Manifest`` carries the name (``[a-z][a-z0-9_-]*``), semver version, title,
generated input and output schemas, ``Idempotent``, ``Requires`` capabilities
(``bin:git`` style) and ``TargetMode`` (``none``, ``single_node``,
``inventory``). ``Manifest.Validate`` checks it and ``Manifest.Checksum`` gives
the digest the controller uses to detect schema conflicts between agents.

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

``RunContract`` checks that the manifest is valid and its schemas marshal, that
``Validate`` rejects malformed input with a ``validation`` error, that
``Execute`` succeeds for every case and returns a JSON object, that no secret
value from ``Case.Secrets`` leaks into logs, outputs or errors (the fake
emitter does not mask, so leaks are visible), and that ``Execute`` returns
promptly with a ``cancelled`` error for an already-cancelled context.
``CheckSource`` statically checks the ``Plugins()`` export, the absence of
``init()`` and mutable package state, and the import rules.

Validating outputs against ``OutputSchema`` needs a schema validator and is not
part of v0.

Running the suite
-----------------

.. code-block:: console

   $ make plugin-contract

runs the SDK tests, the contract suite over every built-in plugin
(``plugins/all``), and the ``examples/plugins/hello`` tests and build.
