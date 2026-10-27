ADR-0032: Runtime-loaded plugins (Go ``.so``)
================================================================================

:Status: Proposed
:Date: 2026-10-27

Context
-------

v1 compiles all plugins in (:doc:`0014-compiled-in-plugins-with-a-public-sdk`).
Some users will want to drop a plugin onto a worker without building a custom
binary. The design keeps that possible: plugins export
``Plugins() []pluginsdk.Plugin``, register through the registry interface, have
no ``init()`` and no package-level state, and the SDK has minimal dependencies.
``CLAUDE.md`` forbids runtime loading without an ADR; ARCHITECTURE §6 asks for
its constraints to be recorded:

- **Build lock-step**: a ``.so`` loads only with the same Go toolchain, the same
  versions of all shared packages and compatible build flags. Plugins would be
  rebuilt for every Damson release (a ``damson plugin build`` command would pin
  this).
- **cgo and dynamic linking**: required; Linux and macOS only (no Windows
  agents); a separate agent build flavor beside the static default.
- **No isolation or unloading**: panics can be recovered per execution, but
  crashes in cgo code or memory corruption take the agent down; unloading needs
  an agent restart.

Decision
--------

Not decided yet. This ADR records the question and the options so they can be
evaluated; it changes nothing until it is accepted. Not before v1 ships.

Alternatives Considered
-----------------------

Compiled in only (status quo)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Static binary; no lock-step problems.

Disadvantages:

- Custom binaries for third-party plugins.

Go ``plugin`` package (``.so``)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- No IPC; same SDK.

Disadvantages:

- All constraints listed in Context.

Out-of-process plugins
~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Isolation.

Disadvantages:

- Rejected in ADR-0014 for v1 (gRPC boilerplate, unneeded multi-language support).

Decision Criteria
-----------------

- Operational burden of lock-step builds.
- Platform coverage (Windows agents).
- Failure isolation.

Rationale
---------

Pending the decision.

Consequences
------------

Positive
~~~~~~~~

- To be recorded with the decision.

Negative
~~~~~~~~

- To be recorded with the decision.

Risks
~~~~~

- Deciding late may force changes to already-shipped behaviour; see Context.

Follow-up
~~~~~~~~~

- Keep the ``Plugins()`` export and the no-``init()`` rule in every plugin until this is decided.

Validation
----------

A proof of concept loading ``examples/plugins/hello`` as a ``.so`` across a patch release of Damson.

References
----------

- :doc:`/architecture/design` §6
- :doc:`0014-compiled-in-plugins-with-a-public-sdk`
