Continuous integration
======================

CI is the GitHub Actions workflow :repo:`.github/workflows/ci.yml`, plus the
separate Windows workflow described at the end of this page. It
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
   * - ``chaos``
     - ``make chaos GOTESTFLAGS=-v``: the HA chaos tests (``internal/chaos``,
       build tag ``chaos``, ``-race``). Several in-process controllers on one
       PostgreSQL; agents are moved between them or cut off mid-step.
   * - ``integration``
     - ``make test-integration GOTESTFLAGS=-v``: non-Postgres
       ``integration``-tagged tests (currently ``plugins/ssh`` against an
       OpenSSH container) through the runner's Docker daemon. Skipped tests or
       a run without ``PASS`` lines fail the job. ``make check`` does not run it.
   * - ``web``
     - ``make web`` (build), ``make web-check`` (prettier, eslint,
       svelte-check, vitest unit tests via ``npm run test:unit``) and the Playwright e2e tests, using the Google Chrome
       preinstalled on the runner (``CHROME_PATH``). The e2e tests build and
       run ``damson dev``, so the job also installs Go.
   * - ``generated``
     - ``make gen`` followed by ``git diff --exit-code``: fails when generated
       code was hand-edited or not regenerated. It covers whatever ``make gen``
       covers: sqlc, ``web/openapi.json`` and the TypeScript API client (the job
       installs Node 22 for ``openapi-typescript``); plugin schemas must be
       added to ``make gen`` when they exist.
   * - ``scripts``
     - ``make bundle-check`` (``deploy/bundle/test.sh``: checksum and
       signature checks of ``verify.sh``, needs ``ssh-keygen``) and
       ``make backup-check`` (``deploy/backup/test.sh``: backup and restore
       round trip with ``sqlite3`` and a ``postgres:18`` service).
   * - ``plugin-contract``
     - ``make plugin-contract``.
   * - ``docs``
     - ``make docs-check`` (strict Sphinx build and linkcheck, uv project in
       ``docs/``).
   * - ``helm``
     - ``make helm-check``: ``helm lint --strict`` and ``helm template`` of the
       chart in ``deploy/helm/damson`` with every values file in its ``ci/``
       directory, plus the checks in ``deploy/helm/check.sh`` (no credential in the
       ConfigMap, agent and controller ServiceAccounts differ, negative cases)
       and ``TestHelmRenderedConfig``, which loads each rendered ``config.toml``
       with ``config.Load`` (see :doc:`/reference/helm`). Installs the pinned helm release. Not a
       required status check yet.
   * - ``systemd``
     - ``make systemd-check``: ``systemd-analyze verify`` of the units in
       ``deploy/systemd``, a check that the core hardening lines are present
       and that the example environment files hold only placeholders (see
       :doc:`/reference/systemd`).
   * - Bundle workflow
     - ``.github/workflows/bundle.yml``: builds the offline bundle with
       ``make bundle`` and verifies its checksums and contents. Runs on pull
       requests touching ``deploy/bundle`` or ``deploy/systemd`` and on manual
       dispatch (``with_images`` adds the container images). It signs the
       bundle with a throwaway key to prove that signing and
       ``verify.sh --pubkey`` work; it is not a release signature. See
       :doc:`/reference/offline-bundle`.

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

Images
------

:repo:`.github/workflows/images.yml` builds the three targets of
:repo:`deploy/docker/Dockerfile` (``damson``, ``damson-agent``,
``damson-agent-build``) with ``docker build`` on pull requests that touch the
Dockerfile or the workflow, and on demand, and runs each tool once (``git``,
``ansible-playbook``, ``ssh``, ``buildah``, ``helm``). Nothing is pushed.

WinRM on Windows
----------------

:repo:`.github/workflows/winrm-windows.yml` runs ``plugins/winrm/windows_test.go``
(build tag ``winrmwindows``) on ``windows-latest`` against a real WinRM service:
it creates a local administrator, a self-signed https listener on port 5986 with
NTLM, and passes ``DAMSON_WINRM_HOST``, ``DAMSON_WINRM_PORT``,
``DAMSON_WINRM_USER``, ``DAMSON_WINRM_PASSWORD`` and ``DAMSON_WINRM_CA_FILE`` to
the test. It runs nightly, on ``workflow_dispatch`` and on pull requests that
touch ``plugins/winrm``. It is not a required status check. To run it by hand::

   gh workflow run "WinRM on Windows" --ref <branch>

What it covers is listed in :doc:`/reference/plugin-winrm`.
