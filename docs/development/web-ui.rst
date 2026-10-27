Web UI
======

The web UI is a SvelteKit single-page app in ``web/``: Svelte 5 (runes),
TypeScript (strict), Tailwind v4 and shadcn-svelte, built with
``adapter-static`` as an SPA (``ssr = false``). The binary embeds the build
output and serves it from every controller (:doc:`/architecture/design` §15).

Building
--------

.. code-block:: console

   $ make web          # npm ci + vite build into web/build
   $ make web-check    # prettier + eslint + svelte-check
   $ cd web && npm run dev      # Vite dev server with hot reload
   $ cd web && npm run test:e2e # Playwright tests (builds and previews first)

``make check`` runs ``make web-check`` automatically when ``web/node_modules``
exists, so Go-only contributors do not need Node.js.

Node.js 22 or newer and npm are required. ``web/package-lock.json`` is
committed; use ``npm ci`` for reproducible installs. The Playwright tests use
the Chrome installed in the devcontainer (``/opt/google/chrome/chrome``, or
``CHROME_PATH``); do not run ``npx playwright install``.

Embedding
---------

``web/embed.go`` embeds ``web/build`` with ``go:embed all:build``, and
``internal/webui`` turns it into an ``http.Handler``:

* existing files are served as-is; files under ``_app/immutable/`` are cached
  for a year because their names are content-hashed;
* a missing ``_app/`` path or a missing path with a file extension is a 404;
* every other unknown path returns ``index.html`` (SPA fallback), so deep links
  survive a reload;
* only ``GET`` and ``HEAD`` are accepted.

``web/build`` is not committed. Only ``web/build/.gitkeep`` is, so the embed
pattern is valid on a fresh checkout and ``go build`` and ``go test`` work
without Node.js. In that case the embedded tree has no ``index.html`` and the
handler answers ``503`` with a hint to run ``make web``. Release builds must
run ``make web`` before ``go build``.

Mount the handler last, after the API and ``/healthz`` routes, so it only sees
requests they did not claim. The controller HTTP server that does this arrives
in a later M0 issue.

Components and conventions
--------------------------

* Use shadcn-svelte components from ``web/src/lib/components/ui``; add more
  with ``npx shadcn-svelte@1.7.1 add <name>`` (the CLI version used to
  generate the current components; bump it deliberately). These files are vendored and
  excluded from prettier and eslint.
* shadcn-svelte imports from ``$lib``. SvelteKit 3 deprecates the ``$lib``
  alias in favour of subpath imports, so ``vite.config.ts`` declares it
  explicitly; the build prints a deprecation notice until the upstream
  components move to subpath imports.
* ``components.json`` and the ``nova`` style were chosen by hand, because
  ``shadcn-svelte init`` is not scriptable here.
* There is no ``svelte.config.js``: the SvelteKit config is inlined in
  ``vite.config.ts``. Drop the explicit ``$lib`` alias workaround there once
  the upstream components use subpath imports.
* ``web/package.json`` sets ``engines.node`` to ``>=22``, enforced by
  ``engine-strict`` in ``web/.npmrc``.
* Call the API only through the generated client, and render schema-driven
  forms only through ``SchemaForm.svelte`` (CLAUDE.md, rule 10).
