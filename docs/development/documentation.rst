Writing documentation
=====================

Documentation is part of every change, not a later phase. A feature is not
done until its documentation is updated in the same change.

Toolchain
---------

The site is built with Sphinx from reStructuredText
(:doc:`/architecture/decisions/0001-sphinx-and-restructuredtext-for-project-documentation`).
``docs/`` is a self-contained Python project managed with
`uv <https://docs.astral.sh/uv/>`_; it is not part of the Go module.

.. code-block:: console

   $ make docs          # build into docs/_build/html
   $ make docs-serve    # live preview on http://127.0.0.1:8000
   $ make docs-check    # strict build (warnings are errors) + linkcheck

``make docs-check`` must pass before committing documentation changes.

To change documentation dependencies, edit ``docs/pyproject.toml`` and run
``uv lock`` in ``docs/``; check the license against
:doc:`/architecture/decisions/0021-dependency-license-policy`.

Where pages go
--------------

.. list-table::
   :header-rows: 1
   :widths: 25 75

   * - Directory
     - Contents
   * - ``concepts/``
     - The model: projects, jobs, runs, agents, plugins, secrets, access.
   * - ``architecture/``
     - Overview, components, data, security; ``design`` renders
       ``ARCHITECTURE.md``.
   * - ``architecture/decisions/``
     - ADRs.
   * - ``development/``
     - Contributor workflow.

``getting-started/``, ``how-to/``, ``reference/``, ``use-cases/`` and
``features/`` are added when there is working behaviour to describe. Do not
document behaviour that has not been verified against the code.

Every page must be in a ``toctree``; an orphan page fails ``docs-check``.

``ARCHITECTURE.md`` is the source of truth for the design. Change it there; the
architecture summary pages link to its sections and must not contradict it.

reStructuredText conventions
----------------------------

Heading adornments, in order: ``=`` (page title), ``-`` (section), ``~``
(subsection).

Cross-reference with roles so the build checks them:

.. code-block:: rst

   :doc:`/architecture/overview`
   :doc:`the overview </architecture/overview>`
   :ref:`some-label`
   :repo:`CLAUDE.md`
   :issue:`123`

Labels are global; prefix them with the page path, for example
``.. _reference-config-server:``.

Prefer ``literalinclude`` of real files over copied snippets. Diagrams use
``.. mermaid::``; tabs and cards come from ``sphinx-design``.

Architecture decision records
-----------------------------

ADRs live in ``docs/architecture/decisions/NNNN-short-title.rst``.

Write an ADR for a decision that affects system structure, adds or removes a
major technology or dependency, establishes a convention, affects
reliability, security or operations, or is expensive to reverse. Do not write
ADRs for implementation details.

Process:

#. Check :doc:`/architecture/decisions/index` for an existing ADR on the topic;
   update or supersede it rather than duplicating.
#. Take the next free number. Numbers are never reused.
#. Use the sections Context, Decision, Alternatives Considered, Decision
   Criteria, Rationale, Consequences (Positive, Negative, Risks, Follow-up),
   Validation and References.
#. Set the status: ``Proposed``, ``Accepted``, ``Rejected``, ``Deprecated`` or
   ``Superseded``.
#. Add it to the table and ``toctree`` in ``decisions/index.rst``.
#. If it changes the design, update ``ARCHITECTURE.md`` in the same change.

To supersede an ADR, write a new one, set the old one's status to
``Superseded`` and link both ways. Do not rewrite an accepted ADR's history.
