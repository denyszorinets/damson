Damson
======

Damson is an open-source (GPL-3.0-or-later) job orchestrator in the spirit of
Rundeck and a modern, minimal Jenkins. Jobs are JSON specs made of steps; each
step calls a plugin. Steps run on stateless agents that connect out to a highly
available controller tier. PostgreSQL is the only required infrastructure.

.. note::

   **Status: design and skeleton (milestone M0).** The documentation describes
   the v1 design. Nothing is released yet.

.. grid:: 1 2 2 2
   :gutter: 2

   .. grid-item-card:: Concepts
      :link: concepts/index
      :link-type: doc

      Projects, jobs, runs, agents, plugins, secrets and access.

   .. grid-item-card:: Tutorial
      :link: tutorial/index
      :link-type: doc

      Run a job over ssh end to end on a development setup.

   .. grid-item-card:: Architecture
      :link: architecture/index
      :link-type: doc

      Overview, components, data flows, security and the full specification.

   .. grid-item-card:: Decisions
      :link: architecture/decisions/index
      :link-type: doc

      Architecture decision records: what was chosen and why.

   .. grid-item-card:: Reference
      :link: reference/index
      :link-type: doc

      Configuration keys and the command line.

   .. grid-item-card:: Development
      :link: development/index
      :link-type: doc

      Contributor workflow and how to write documentation.

.. toctree::
   :hidden:
   :maxdepth: 2

   concepts/index
   tutorial/index
   architecture/index
   reference/index
   development/index
