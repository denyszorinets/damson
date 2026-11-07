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
  ``map[string]any``, ``credential`` ref, ``host_key``, ``host_key_policy``). ``Port`` is the node's own port; ``0`` means the plugin's
  default (``winrm`` uses 5986 for https and 5985 for http).
- **Host keys and config:** ``ExecRequest.HostKeys`` (a ``HostKeyReporter``,
  may be nil) lets a plugin report the host key it observed on a node, in
  ``authorized_keys`` format; the agent forwards it so the controller can store
  it on the node (trust on first use). ``ExecRequest.Config`` carries operator
  settings for the plugin (for example ``allow_insecure_host_keys`` for ``ssh`` and ``transfer``);
  absent keys mean the safe default. Both were added in SDK 0.2.0 and are
  optional, so existing plugins are unaffected.
- ``Validate`` failures must be ``validation`` errors; they are never retried.

Manifest
--------

``Manifest`` carries the name (``[a-z][a-z0-9_-]*``), semver version, title,
generated input and output schemas, ``Idempotent``, ``Requires`` capabilities
(``bin:git`` style) and ``TargetMode`` (``none``, ``single_node``,
``inventory``, ``nodes``). ``NodePaths`` (SDK 0.3.0, mode ``nodes`` only) lists 1 to 8 unique JSON
pointers into the input that each name a node, for example
``/source/node/name``; ``Manifest.NodeNames(input)`` returns the names. See
:doc:`/architecture/decisions/0048-plugins-that-target-several-nodes`.
``Manifest.Validate`` checks it and ``Manifest.Checksum`` gives
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
  leaks are visible. Every bundle field counts as a secret here, so the check
  is deliberately stricter than the agent, which does not mask the S3
  ``endpoint``, ``region`` and ``path_style`` (ADR-0051). The check is **plaintext only**; base64, URL-encoded and
  per-line variants are the agent masker's job (ARCHITECTURE section 9.4);
- ``Execute`` returns promptly with a ``cancelled`` error for an
  already-cancelled context and, when ``Options.Blocking`` is set to an
  execution that runs until cancelled, for a running one that is cancelled
  after a short delay. A plugin that ignores cancellation fails after
  ``Options.Timeout``.

``plugintest.Case.Config`` is passed as ``ExecRequest.Config``, and
``Outcome.HostKeys`` (a ``FakeHostKeys``) records the host keys a plugin
reported. ``plugintest.Exec`` recovers panics (reported as an ``internal`` error) and
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
(``plugins/all``), and the ``examples/plugins/hello`` vet and tests. It then
builds ``examples/plugins/hello`` into a custom binary with ``damson build``
(offline, ``GOPROXY=off`` after ``go mod download``) and runs
``plugin validate``, ``plugin run`` and ``version`` on it.
``examples/plugins/hello`` is a nested Go module, so ``go test ./...`` at the
repository root does not cover it.

Building a custom binary
------------------------

An out-of-tree plugin module exports ``Plugins()`` from its root package and
is linked in with ``damson build --with`` (:ref:`cli-build`):

.. code-block:: console

   $ damson build --with example.com/damson-plugin-hello@v1.2.0 -o ./damson-agent

The result is a normal Damson binary that also reports the plugin in
``agent.hello`` when run as an agent. The plugin name may not equal a built-in
name at any version, and ``name@version`` must be unique; a violation stops the
binary at start. The controller links only the built-in plugins, so extra
plugins are agent plugins.

Linked plugins are not sandboxed: they run in the agent process and can read
every secret dispatched to the agent, its token and files, and their package
initialisers run at start. Only link code you have reviewed, pin versions, keep
the Go checksum database on, and keep extras out of controller binaries
(:doc:`/architecture/decisions/0050-custom-binaries-with-damson-build`).

Developer tooling
-----------------

``damson plugin`` (:ref:`cli-plugin`) covers the author's loop on the plugins
linked into the binary:

#. ``damson plugin new <name>`` scaffolds a module whose tests already pass.
#. In the new directory run ``go get github.com/denyszorinets/damson@<version>``,
   ``go mod tidy`` and ``go test ./...``.
#. Link the plugin into a binary with ``damson build --with <module>@<version>``
   (:ref:`cli-build`).
#. ``damson plugin validate <name>`` checks the manifest and schemas.
#. ``damson plugin run <name> --input input.json [--secrets s.json] [--nodes n.json]``
   runs it once without a controller, with the agent's masking applied.

Shared git checkout
-------------------

The ``ansible``, ``buildah`` and ``helm`` plugins clone with
``plugins/internal/gitcheckout`` (built-in plugins only; it is not public API).
``gitcheckout.Clone(ctx, dir, repo, auth, opts)`` needs the ``git`` binary
(capability ``bin:git``) and does a depth-1 fetch of ``repo.Ref`` (a branch, a
tag or a full 40-hex commit; empty means the default branch) into ``dir``,
checks it out detached and returns the resolved commit. Submodules are cloned
only when asked.

Rules the helper enforces:

* Only ``https`` and ``ssh`` URLs (scp-like ``host:path`` is ssh). ``http``,
  ``git`` and ``file`` need ``Repo.AllowInsecure``, which the plugins set only
  from an operator setting. ``ext::``, ``fd::`` and local paths are refused, as
  are URLs or refs that start with ``-`` and URLs that carry a password.
* Credentials never appear in an argument, a URL, the environment or a log
  line, and a URL that carries a user, token or password is refused. A token is
  a 0600 file read by a git credential helper that answers **only for the
  repository's own host**: a redirect or a submodule on another host gets no
  credential. An SSH key is a 0600 file named by ``GIT_SSH_COMMAND``. Both live
  in a private directory under the system temporary directory (``TMPDIR``),
  removed before ``Clone`` returns; a crash can leave it behind, so run agents
  with a private tmpfs ``TMPDIR`` (ARCHITECTURE 6.4 asks for tmpfs where
  available). The token is not written to ``.git/config``.
* ssh always ignores the agent user's ``~/.ssh`` config and keys (``-F
  /dev/null``, ``IdentitiesOnly``, no default keys, no control master) and
  offers only the given key. It needs ``Auth.KnownHosts`` to verify the host, or
  an explicit ``Auth.InsecureHostKey`` (the plugins take that from
  ``agent.allow_insecure_host_keys``); with neither it fails closed. This also
  holds for an ssh submodule of an https repository.
* ``dir`` must be empty; git runs with no templates, so no hooks are installed.
* git runs with no user or system config, no prompts and an explicit protocol
  allow-list. Cancellation kills git's whole process group; ``Options.Timeout``
  gives a ``timeout`` error.
* Errors map by git's output to ``auth``, ``connection``, ``validation`` (bad ref
  or repository) or ``exit_code``.

The token helper is a small POSIX ``sh`` function, so token credentials need
agents on Linux or macOS (the agent images are Linux).
