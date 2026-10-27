Contributor workflow
====================

Milestones and vertical slices
------------------------------

Work proceeds in milestones M0–M9 (:doc:`/architecture/design` §21). Each
milestone is a **vertical slice**: it is done when it works end to end with
tests, not when one layer is complete. The current milestone is recorded in
:repo:`CLAUDE.md` (``CURRENT:`` line).

.. list-table::
   :header-rows: 1
   :widths: 8 25 67

   * - #
     - Slice
     - Done when
   * - M0
     - Skeleton
     - Binary and config, both store backends and migrations, ``/healthz``, CI,
       embedded empty SPA, ``pkg/pluginsdk`` v0 with ``plugintest``.
   * - M1
     - Agent link
     - WebSocket JSON-RPC, pool tokens, hello/heartbeat, reconnect, agents page.
   * - M2
     - First run end to end
     - Linear specs, ``ssh`` plugin, registry, dispatch with leases and
       fencing, live logs, run page, API trigger.
   * - M3
     - Engine complete + HA
     - Full control flow, multi-controller, leader leases, graph view, chaos
       test green.
   * - M4–M9
     - …
     - Secrets and inventory; transfer and WinRM; Ansible, Buildah, Helm;
       triggers; auth; production readiness.

Rules that are easy to break
----------------------------

The full list is in :repo:`CLAUDE.md`. The ones that most often matter:

- **The engine is pure.** No I/O, no ``time.Now()``, no randomness in
  ``internal/engine``. Every rule in §5.3 has table or golden tests.
- **Every state transition is a CAS** with a rows-affected check.
- **Fencing everywhere.** Agent reports apply only when ``attempt_id`` matches.
- **Both databases, always.** Store tests run on SQLite and PostgreSQL.
- **Secrets** are never logged, persisted or returned; new bundle code paths
  need a masking test.
- **Generated code is never hand-edited** (sqlc, TS client, plugin schemas):
  change the source and run ``make gen``.
- **Protocol changes** update ARCHITECTURE §7.2 and stay compatible with N-1.
- **New dependencies** need a license check
  (:doc:`/architecture/decisions/0021-dependency-license-policy`).
- **No new infrastructure** (brokers, caches, services) without an ADR.

Changes that need a decision first
----------------------------------

Ask before changing the job spec format, the agent protocol, the database
schema or anything security related; record significant decisions as ADRs
(:doc:`documentation`).

Make targets
------------

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - Target
     - Purpose
   * - ``make docs``
     - Build the documentation site.
   * - ``make docs-serve``
     - Live-reloading documentation preview.
   * - ``make docs-check``
     - Strict documentation build and linkcheck.

.. note::

   The Go and web targets listed in :repo:`CLAUDE.md` (``make check``,
   ``make gen``, ``make dev``, ``make test-pg`` and others) are added with the
   M0 skeleton.
