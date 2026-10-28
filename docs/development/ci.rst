Continuous integration
======================

CI is a single GitHub Actions workflow, :repo:`.github/workflows/ci.yml`. It
runs on pull requests to ``develop`` and ``main`` and on pushes to those
branches. It mirrors ``make check`` and adds the checks that need Docker, a
clean checkout or extra tooling. Every job sets ``CI=1``, so ``make`` targets
that print ``SKIPPED`` locally (``lint``, ``web-check``) fail instead.

Jobs
----

.. list-table::
   :header-rows: 1
   :widths: 20 80

   * - Job
     - What it runs
   * - ``go``
     - ``make fmt-check``, ``make vet``, golangci-lint (official action,
       configuration in :repo:`.golangci.yml`, same as ``make lint``) and
       ``make test GOTESTFLAGS=-race`` (SQLite store).
   * - ``postgres``
     - ``make test-pg GOTESTFLAGS=-v``: every ``integration``-tagged test
       (store, api, controller, cmd) against PostgreSQL
       started by testcontainers through the runner's Docker daemon. Verbose,
       so a skipped or failing test is visible in the log.
   * - ``integration``
     - ``make test-integration GOTESTFLAGS=-v``: non-Postgres
       ``integration``-tagged tests (currently ``plugins/ssh`` against an
       OpenSSH container) through the runner's Docker daemon. Skipped tests or
       a run without ``PASS`` lines fail the job. ``make check`` does not run it.
   * - ``web``
     - ``make web`` (build), ``make web-check`` (prettier, eslint,
       svelte-check) and the Playwright e2e tests, using the Google Chrome
       preinstalled on the runner (``CHROME_PATH``). The e2e tests build and
       run ``damson dev``, so the job also installs Go.
   * - ``generated``
     - ``make gen`` followed by ``git diff --exit-code``: fails when generated
       code was hand-edited or not regenerated. It covers whatever ``make gen``
       covers: sqlc, ``web/openapi.json`` and the TypeScript API client (the job
       installs Node 22 for ``openapi-typescript``); plugin schemas must be
       added to ``make gen`` when they exist.
   * - ``plugin-contract``
     - ``make plugin-contract``.
   * - ``docs``
     - ``make docs-check`` (strict Sphinx build and linkcheck, uv project in
       ``docs/``).

Required status checks
----------------------

Branch protection is configured by a repository admin in the GitHub settings,
not by the workflow. For ``develop`` and ``main`` require these checks, which
use the job display names, so a failing check blocks the merge:

- ``Go (gofmt, vet, golangci-lint, tests on SQLite)``
- ``Store tests on PostgreSQL``
- ``Web (build, lint, typecheck, e2e)``
- ``Generated code is up to date``
- ``Plugin contract suite``
- ``Docs (strict build and linkcheck)``

Renaming a job changes its check name; update the branch protection rule with
it.

Conventions
-----------

- Third-party actions are pinned to a full commit SHA with the version in a
  comment, so a moved tag cannot change what runs. Update them deliberately.
- The workflow has ``permissions: contents: read``; no job writes to the
  repository.
- Go is installed from the ``go`` directive in ``go.mod`` (``go-version-file``),
  so there is one place to bump it. golangci-lint is built with that Go
  (``install-mode: goinstall``) so it can analyse the newest language version.
- Reproduce a CI run locally with the same ``make`` targets. Install
  golangci-lint v2 to get ``make lint`` inside ``make check``; without it the
  local run prints ``SKIPPED lint``.
- Inspect results of a pull request with ``gh pr checks <number>``.
