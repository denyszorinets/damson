ADR-0050: Custom binaries with damson build
================================================================================

:Status: Accepted
:Date: 2026-11-06

Context
-------

Plugins are compiled in (:doc:`0014-compiled-in-plugins-with-a-public-sdk`) and
runtime loading is not decided (:doc:`0032-runtime-loaded-plugins`). A user who
wants a third-party plugin therefore needs a binary that links it.
ARCHITECTURE §6 promised ``damson build --with <module>``, the way ``xcaddy``
works for Caddy. It needs a stable way for generated code to start Damson, a
defined way to catch plugin name clashes, and an explicit statement of what
linking third-party code into a Damson process means for security.

Decision
--------

Public entry point
~~~~~~~~~~~~~~~~~~

``pkg/damsoncli`` is the public entry point of the command line:
``damsoncli.Main(damsoncli.Options{Modules: []damsoncli.Module{{Path, Plugins}}})``.
It is released with Damson's version. Fields may be added to ``Options`` and
``Module`` but never removed. ``cmd/damson`` calls it too, with no modules. The
command line implementation moved from ``cmd/damson`` to ``internal/cli``. This
is a second public Go API besides ``pkg/pluginsdk``. Plugins must not import it.

The build command
~~~~~~~~~~~~~~~~~

``damson build --with module[@version][=dir]... [--damson-version v] [-o file] [--keep-workdir]``
generates a ``main`` package in a temporary module named ``damson-custom-build``
that calls ``damsoncli.Main`` with the extra modules, and builds it.

- It requires Damson at the running binary's module version, read from the
  build information. A development build (no version, ``(devel)``, ``+dirty``
  or a replaced module) needs ``--damson-version`` or
  ``--with github.com/denyszorinets/damson=<dir>``.
- It fails if version selection picks a different Damson version than the one
  requested (a plugin module requires a newer one).
- ``module=dir`` uses a local directory (a ``replace``); a version and a
  directory cannot be combined.

Plugin rules
~~~~~~~~~~~~

The root package of an extra module exports ``Plugins() []pluginsdk.Plugin``.
When the binary starts, for every command:

- an extra plugin name may not equal any built-in name, at any version;
- a plugin name belongs to one module;
- ``name@version`` is unique;
- every manifest must validate and have a checksum;
- a ``Plugins`` function that is missing or panics is an error.

A violation stops the binary at start.

Extras are agent plugins
~~~~~~~~~~~~~~~~~~~~~~~~

The controller registry links only ``plugins/all``. Extra plugins are reported
by agents in ``agent.hello`` like any other. A controller (not ``dev``) started
from a binary with extras logs a warning and starts. It does not refuse,
because refusing would also break ``damson dev``. This is the one security
policy choice in this ADR, together with adding a second public Go API. It needs
maintainer sign-off.

Build process
~~~~~~~~~~~~~

The build runs only the Go toolchain, never a shell:

- ``go get`` for remote modules, then ``go list`` (to check the selected Damson
  version), then ``go build -mod=mod -trimpath``. There is no ``go mod tidy``;
  it needs the whole module graph.
- ``CGO_ENABLED=0`` unless the caller sets it, and ``GOWORK=off``.
- It never sets ``GOSUMDB``, ``GONOSUMDB``, ``GOINSECURE`` or ``GOFLAGS``, and
  warns if the caller weakened module verification.
- ``go.sum`` is seeded only from the ``go.sum`` of local ``=dir`` modules the
  user named (a ``go.sum`` line makes Go skip the checksum database for that
  module). That is acceptable because the local directory's code is trusted
  anyway.
- The output is written next to the target through an exclusively created
  temporary file and atomically renamed. A failed
  build leaves no output.

Trust
~~~~~

Extra modules are fully trusted code in the process. Their ``init`` functions
and package initialisers run at start. On an agent such code can read every
secret dispatched to any step on that agent, the agent token and its files. On
a controller it can read the database DSN, the KEK and all secrets. Masking
does not help against code that runs in the process.

Recommendations:

- link extras into agent binaries only and run the stock binary as controller;
- pin versions instead of ``latest``;
- review the module and its dependencies;
- keep the Go checksum database on.

Alternatives Considered
-----------------------

- ``go build -overlay`` over ``plugins/all``: needs a Damson source tree and
  hides what is linked.
- A generated ``plugins/all`` in a fork: every user maintains a fork and
  rebases on each release.
- Runtime loading: not decided (:doc:`0032-runtime-loaded-plugins`).

Decision Criteria
-----------------

- Simple for operators: one command, no Damson source tree.
- Honest about trust: no sandbox is claimed.
- Reproducible and safe by default: version-pinned, checksum database on.
- Compatible with a future runtime loader (``Plugins()`` is unchanged).

Rationale
---------

A generated ``main`` that calls a public entry point is the smallest design
that needs no source checkout. Name rules at start catch clashes on every
command, not only at build time, so a binary built elsewhere cannot silently
shadow a built-in.

Consequences
------------

Positive
~~~~~~~~

- Third-party plugins can be used without forking.
- A name clash cannot be missed.

Negative
~~~~~~~~

- ``pkg/damsoncli`` is a second public API to keep compatible.
- ``examples/plugins/hello`` is now a nested Go module, so ``./...`` at the
  repository root no longer covers it. ``make plugin-contract`` vets and tests
  it and builds it into a custom binary offline (``GOPROXY=off`` after
  ``go mod download``).
- A custom binary needs the Go toolchain at build time.

Risks
~~~~~

- Extras run with the full privileges of the process (see Trust). Mitigation:
  documented recommendations and the controller warning.

Follow-up
~~~~~~~~~

- Maintainer sign-off on the controller policy (warn, not refuse) and the
  second public API.

Validation
----------

Unit tests in ``internal/buildcli`` and ``internal/cli`` (link rules);
``make plugin-contract`` builds ``examples/plugins/hello`` into a custom binary,
validates and runs the plugin and checks ``damson version``.

References
----------

- :doc:`0014-compiled-in-plugins-with-a-public-sdk`
- :doc:`0032-runtime-loaded-plugins`
- :doc:`/architecture/design` §6
- :doc:`/reference/cli` (``damson build``)
