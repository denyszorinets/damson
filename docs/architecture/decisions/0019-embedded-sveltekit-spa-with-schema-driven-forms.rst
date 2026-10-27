ADR-0019: Embedded SvelteKit SPA with schema-driven forms
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

The UI must work air-gapped, be served by every controller, and render forms
for any plugin (including third-party plugins in custom agent binaries) without
plugin-specific frontend code.

Decision
--------

- **SvelteKit** SPA (``adapter-static``, ``ssr = false``), Svelte 5 runes,
  TypeScript strict, Tailwind v4; built into ``web/build`` and embedded with
  ``go:embed``.
- **shadcn-svelte** components vendored in ``web/src/lib/components/ui``.
- **Schema forms** with ``@sjsf/form`` (shadcn theme, ajv8), always wrapped by
  ``SchemaForm.svelte`` so the library can be swapped (single maintainer).
- **CodeMirror 6** JSON job editor validated against the spec and plugin
  schemas.
- No plugin ships custom UI in v1.
- Live data through one ``lib/sse.ts`` helper with reconnect and ``after_seq``
  resume; a virtualized log viewer with ANSI rendering.

Alternatives Considered
-----------------------

React (Next.js or Vite)
~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Larger bundles and more boilerplate; no advantage for an embedded SPA.

Server-rendered HTML (htmx, templ)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Live graphs, virtualized logs and schema forms are much harder.

Per-plugin custom UI components
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Plugins in custom binaries would need frontend builds; breaks the
  schema-only model.

Decision Criteria
-----------------

Air-gap, bundle size, schema-driven UI, licensing (all MIT).

Rationale
---------

A static SPA embedded in the binary needs nothing at runtime; schema-driven
forms mean new plugins get a UI for free.

Consequences
------------

Positive
~~~~~~~~

- Every controller serves the UI; no CDN.

Negative
~~~~~~~~

- Complex plugin inputs get generic forms only.

Risks
~~~~~

- ``@sjsf/form`` abandonment. Mitigation: the ``SchemaForm.svelte`` wrapper.

Follow-up
~~~~~~~~~

- M0: embedded empty SPA. v1.1: visual step-form editor.

Validation
----------

Playwright smoke tests: login, trigger a run, watch logs.

References
----------

- :doc:`/architecture/design` §15
