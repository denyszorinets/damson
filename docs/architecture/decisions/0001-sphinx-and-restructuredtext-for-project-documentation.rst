ADR-0001: Sphinx and reStructuredText for project documentation
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Damson needs operator, user, contributor and architecture documentation,
including ADRs. The repository had conflicting guidance: ``CLAUDE.md``,
``ARCHITECTURE.md`` §19 and the ``doc-sync`` agent asked for Markdown ADRs in
``docs/adr/``, while the project's documentation and architecture skills asked
for a Sphinx site written in reStructuredText.

Requirements:

- Cross-references between pages, ADRs and labelled sections must be checked at
  build time, so a renamed page cannot leave dead links behind.
- Examples should be included from real files, not copied, so they cannot drift.
- The site must be publishable as static HTML (GitHub Pages) and must not need a
  service at runtime.
- ``ARCHITECTURE.md`` stays the source of truth for the design and must not be
  duplicated by hand.
- The documentation toolchain must not leak into the Go module or the shipped
  binary.

Decision
--------

The documentation is a **Sphinx** site written in **reStructuredText**.

- ``docs/`` is the Sphinx source root and a self-contained Python project
  (``pyproject.toml``, ``uv.lock``, ``conf.py``), managed with ``uv``. It is not
  part of the Go module.
- ADRs live in ``docs/architecture/decisions/NNNN-title.rst`` using the format
  defined by the architecture skill.
- ``ARCHITECTURE.md`` is rendered into the site through ``myst-parser``
  (``docs/architecture/design.rst`` includes it). ``myst-parser`` is used only
  for that include; new pages are ``.rst``.
- ``make docs``, ``make docs-serve`` and ``make docs-check`` build, preview and
  strictly check the site. ``make docs-check`` (warnings are errors, plus
  linkcheck) must pass before documentation changes are committed.
- Theme: Furo. Extensions: ``sphinx-design`` (tabs, cards),
  ``sphinxcontrib-mermaid`` (diagrams), ``sphinx.ext.extlinks`` (``:repo:`` and
  ``:issue:`` roles).

Alternatives Considered
-----------------------

Markdown files in ``docs/adr/``
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Plain Markdown, rendered by GitHub.

Advantages:

- No toolchain; familiar to every contributor.

Disadvantages:

- No build-time check of cross-references or includes; links rot silently.
- No published site with navigation and search without adding a generator anyway.

MkDocs (Material) with Markdown
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Markdown authoring, attractive default theme.

Disadvantages:

- Cross-reference checking is weaker than Sphinx's ``:doc:`` and ``:ref:``
  roles.
- No equivalent of ``literalinclude`` with line ranges and checked
  ``:ref:`` labels without plugins.

Sphinx with MyST Markdown for all pages
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Sphinx features with Markdown syntax.

Disadvantages:

- Two syntaxes inside one tree (directive syntax in MyST is reST in disguise);
  the project skills standardize on reST.

Decision Criteria
-----------------

- Verifiable cross-references and includes (strict build in CI).
- Static output with no runtime service.
- No coupling to the Go build.
- Long-term maintenance of the toolchain.

Rationale
---------

The design is large and heavily cross-referenced (spec rules, protocol, ADRs).
A strict Sphinx build turns broken references into CI failures, which matters
more than authoring convenience. Including ``ARCHITECTURE.md`` instead of
porting it keeps a single source of truth.

Consequences
------------

Positive
~~~~~~~~

- Broken links and orphan pages fail ``make docs-check``.
- ``ARCHITECTURE.md`` is published without duplication.

Negative
~~~~~~~~

- Contributors need ``uv`` and Python to build the docs locally.
- reST is less familiar than Markdown.

Risks
~~~~~

- Drift between the summary pages under ``docs/architecture/`` and
  ``ARCHITECTURE.md``. Mitigation: summary pages link to the authoritative
  section and stay short.

Follow-up
~~~~~~~~~

- Add ``make docs-check`` to CI and publish to GitHub Pages once the repository
  is public (M0).
- Turn on linkcheck for repository links once the GitHub org is settled
  (:doc:`0023-project-name-and-namespace`).

Validation
----------

``make docs-check`` passes on the initial site (2026-10-27).

References
----------

- :doc:`/development/documentation`
- :doc:`/architecture/design`
