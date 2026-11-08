.. _operations-index:

Operations
==========

This guide is for the people who install, upgrade and run Damson in
production. It links to the reference pages for every key and flag instead of
repeating them.

.. note::

   Damson is pre-release (:doc:`/index`). The behaviour below is what the code
   does today. Where the design in :repo:`ARCHITECTURE.md` goes further, the
   page says so; the main gaps are collected in :ref:`operations-known-gaps`.

Choose a setup
--------------

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - Situation
     - Start here
   * - Kubernetes
     - :doc:`/reference/helm`: controllers and agent pools from the Helm chart.
   * - Virtual machines
     - :doc:`/reference/systemd`: hardened systemd units for the controller and
       the agent.
   * - No internet access
     - :doc:`/reference/offline-bundle`: binaries, chart, units and optionally
       images in one archive, then the Kubernetes or VM guide.
   * - Development, one machine
     - :doc:`/tutorial/index`: ``damson dev`` runs a controller and an agent in
       one process with SQLite. Not for production.

Both production setups need PostgreSQL, a few generated secrets and TLS in
front of the controller; :doc:`install` lists them in order.

Pages
-----

.. toctree::
   :maxdepth: 1

   install
   upgrade
   backup
   ha
   monitoring
   hardening
   troubleshooting

.. _operations-checklist:

Production readiness checklist
------------------------------

Tick every line before you send real work through an installation.

- PostgreSQL is a managed or replicated service with backups and point-in-time
  recovery (:doc:`backup`), reachable with ``sslmode=verify-full``.
- At least two controllers run on PostgreSQL, each with its own
  ``controller.internal_url``, the same cluster secret and the same KEK
  (:doc:`ha`).
- Agents connect with ``wss://`` through a TLS-terminating proxy or ingress;
  ``controller.insecure_dev`` is off (:doc:`hardening`).
- The KEK is stored outside the database backup, in at least two places
  (:doc:`backup`).
- Login is OIDC, or ``auth.require_token`` is true. The default is neither
  (:doc:`hardening`).
- The admin token is created, stored in a password manager and used only for
  recovery and administration.
- Each agent pool has its own token and, on Kubernetes, its own ServiceAccount.
- A restore drill has been run once (:doc:`backup`).
- Something probes ``/readyz`` and alerts on it (:doc:`monitoring`).
- The upgrade procedure is written down for your team, including who takes the
  database backup (:doc:`upgrade`).
- You have read :ref:`operations-known-gaps`.

.. _operations-known-gaps:

Known gaps
----------

These are described in the design but not implemented today. Plan around them.

- **No metrics and no traces.** There is no ``/metrics`` endpoint and no
  OpenTelemetry. ARCHITECTURE section 17 describes both. Monitor with
  ``/readyz``, logs and the API (:doc:`monitoring`).
- **No agent drain on SIGTERM.** When an agent stops, running steps are
  cancelled and get 5 seconds. Rollouts, node drains and autoscaler scale-down
  cancel work (:doc:`upgrade`).
- **PostgreSQL is the only HA database.** SQLite supports exactly one
  controller.
- **Rolling upgrades are zero-downtime only if migrations are compatible with
  the old version.** Nothing checks that for you (:doc:`upgrade`).
- **No separate migrate command.** Migrations run at controller start with
  ``--auto-migrate``. ARCHITECTURE section 16 mentions ``damson migrate up``;
  it does not exist.
- **No backup command.** Use your database's tools (:doc:`backup`).
- **The offline bundle is unsigned.** It has checksums only
  (:doc:`/reference/offline-bundle`).
- **No TLS on the controller itself.** Terminate TLS in a proxy or ingress.
  The internal listener between controllers is plain HTTP.
- **Log retention is manual.** Only audit events have a retention setting
  (``audit.retention_days``); runs and logs are kept until purged
  (:doc:`backup`).
