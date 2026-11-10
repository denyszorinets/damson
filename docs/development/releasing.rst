Releasing
=========

How a version such as ``0.1.0`` is cut. Two workflows do the publishing:
**Release** (on a ``v*`` tag) and **Pages** (on every push to ``main``).

One-time setup
--------------

- In the repository settings, Pages must use the source **GitHub Actions**.
- The first push of each image creates a ``ghcr.io`` package that is **private**
  by default. Make ``damson``, ``damson-agent`` and ``damson-agent-build`` public
  in the package settings, or nobody can pull them.

Steps
-----

#. Branch ``release/X.Y.Z`` from ``develop``.
#. In ``CHANGELOG.md`` add a section headed ``## [X.Y.Z] - YYYY-MM-DD``. The
   Release workflow extracts the notes of the GitHub release from it and fails
   when the section is missing.
#. Open a pull request from the release branch to ``main`` and merge it with a
   **merge commit** (not squash).
#. Tag the merge commit on ``main`` and push the tag::

      $ git tag vX.Y.Z && git push origin vX.Y.Z

#. Watch the **Release** workflow (below) and check the release page.
#. Merge ``main`` back into ``develop``.

Release workflow
----------------

Triggered by the tag. It builds the web UI once, then in parallel:

- the ``damson`` binary for linux, darwin (amd64, arm64) and windows (amd64),
  with the tag set as the version (``damson version``);
- the offline bundle (``deploy/bundle/build.sh``), verified after the build;
- the three images, multi-arch (linux/amd64 and linux/arm64), pushed to
  ``ghcr.io/<owner>/`` with the tags ``vX.Y.Z``, ``X.Y.Z`` and ``latest``.

When all succeed it writes ``SHA256SUMS`` over the archives and the bundle and
creates the GitHub release with the changelog notes and all assets. The assets
are not signed.

Running the workflow by hand (**workflow_dispatch**) is a dry run: it builds
everything with the version ``v0.0.0-dryrun`` and publishes nothing (no image
push, no release).

Pages workflow
--------------

Builds the landing page (``site/``) and the Sphinx documentation, and publishes
them to GitHub Pages: the landing page at the root and the docs under ``/docs/``.
It runs on every push to ``main`` (so on each release merge) and on manual runs;
pull requests that touch ``site/`` or ``docs/`` only build it as a check.
