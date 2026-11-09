.. _reference-systemd:

Install on VMs
==============

The units in :repo:`deploy/systemd` run the controller and the agent as
hardened systemd services on ordinary Linux hosts. The binary is static and has
no runtime dependencies (the Ansible plugin needs ``ansible`` on the agent
host). The files are also part of the :doc:`offline bundle <offline-bundle>`.

.. list-table::
   :header-rows: 1
   :widths: 35 65

   * - File
     - Purpose
   * - ``damson-controller.service``
     - Controller, started with ``--auto-migrate``.
   * - ``damson-agent.service``
     - Agent.
   * - ``damson.sysusers``
     - The ``damson`` system user (``systemd-sysusers``).
   * - ``controller.env.example``, ``controller.toml.example``
     - Credentials and configuration of a controller.
   * - ``agent.env.example``, ``agent.toml.example``
     - Pool token and configuration of an agent.

Prerequisites
-------------

- A PostgreSQL database reachable from the controller hosts. SQLite is not for
  this setup: it is for ``damson dev`` and single-controller installs.
- TLS in front of the controller for agents: a TLS-terminating proxy
  (the controller has no TLS settings of its own). Agents connect with ``wss://``. Behind a proxy that
  sets ``X-Forwarded-Proto``, set ``trust_forwarded_proto = true`` in
  ``controller.toml``.
- ``systemd`` on every host.

Install a controller
--------------------

#. Copy the binary (for example ``bin/damson-linux-amd64`` from the bundle):

   .. code-block:: console

      # install -m 0755 damson-linux-amd64 /usr/local/bin/damson

#. Create the service user:

   .. code-block:: console

      # install -m 0644 damson.sysusers /etc/sysusers.d/damson.conf
      # systemd-sysusers

#. Create the configuration. ``controller.env`` holds credentials and must be
   mode ``0600``, owned by root; systemd reads it before it drops privileges:

   .. code-block:: console

      # install -d -m 0755 /etc/damson
      # install -m 0600 -o root -g root controller.env.example /etc/damson/controller.env
      # install -m 0644 controller.toml.example /etc/damson/controller.toml

   Edit both. In ``controller.env`` replace every ``CHANGE-ME``: the database
   DSN, the cluster secret (at least 32 bytes), the key-encryption key
   ``DAMSON_KEK`` (``damson admin kek generate``; back it up, losing it loses
   every stored secret; drop it when ``secrets.backend`` is ``openbao``) and
   ``DAMSON_CONTROLLER__INTERNAL_URL``, the address under which the other
   controllers reach this one.

#. Install the units and start the controller:

   .. code-block:: console

      # install -m 0644 damson-controller.service damson-agent.service /etc/systemd/system/
      # systemctl daemon-reload
      # systemctl enable --now damson-controller

#. Create the admin token and the agent pools and tokens with
   :ref:`damson admin <cli-admin>`. These commands connect to the database
   directly, so they need the controller's environment. Run them as the
   ``damson`` user through a transient unit; root reads the environment file,
   so the credentials never appear on a command line or in your shell history:

   .. code-block:: console

      # systemd-run --pipe --wait --collect -p User=damson \
          -p EnvironmentFile=/etc/damson/controller.env \
          -E DAMSON_CONFIG=/etc/damson/controller.toml \
          /usr/local/bin/damson admin token create --pool default --name vm-agent

   The controller must have run once so that migrations are applied. The token
   is printed once; put it in ``agent.env`` of the agent host.

Multiple controllers
~~~~~~~~~~~~~~~~~~~~

Install the same files on every controller host with the same
``DAMSON_CONTROLLER__CLUSTER_SECRET`` (and the same ``DAMSON_KEK``). Only
``DAMSON_CONTROLLER__INTERNAL_URL`` differs: each host sets its own address.
The unit starts the controller with ``--auto-migrate``, which runs migrations behind a PostgreSQL advisory
lock, so controllers that start together do not race.

Install an agent
----------------

On the agent host, install the binary, the user and the units as above, then:

.. code-block:: console

   # install -d -m 0755 /etc/damson
   # install -m 0600 -o root -g root agent.env.example /etc/damson/agent.env
   # install -m 0644 agent.toml.example /etc/damson/agent.toml
   # systemctl enable --now damson-agent

Put the pool token in ``agent.env`` (``DAMSON_AGENT__TOKEN``) and set
``controller_url`` (``wss://...``), ``pool`` and optionally ``tls_ca_file`` in
``agent.toml``.

Unit hardening
--------------

Both units run as ``damson`` with these directives.

.. list-table::
   :header-rows: 1
   :widths: 35 65

   * - Directive
     - Effect
   * - ``NoNewPrivileges``, ``RestrictSUIDSGID``
     - No privilege gain through setuid or setgid binaries.
   * - ``CapabilityBoundingSet=``, ``AmbientCapabilities=``
     - No Linux capabilities.
   * - ``ProtectSystem=strict``
     - The whole file system is read-only except the state directory,
       ``/tmp`` (private) and ``/dev``.
   * - ``ProtectHome``
     - Home directories are hidden (controller: ``yes``) or read-only
       (agent: ``read-only``).
   * - ``PrivateTmp``, ``PrivateDevices``
     - Own ``/tmp``; no physical devices.
   * - ``ProtectKernelTunables``, ``ProtectKernelModules``,
       ``ProtectKernelLogs``, ``ProtectControlGroups``, ``ProtectClock``,
       ``ProtectHostname``
     - Kernel settings, modules, logs, cgroups, clock and hostname cannot be
       changed.
   * - ``ProtectProc=invisible``, ``ProcSubset=pid``
     - Controller only: other users' processes are hidden in ``/proc``.
   * - ``RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX``
     - IP and Unix sockets only.
   * - ``RestrictNamespaces``, ``RestrictRealtime``, ``LockPersonality``
     - No new namespaces, no realtime scheduling, no personality changes.
   * - ``SystemCallArchitectures=native``
     - No foreign-architecture system calls.
   * - ``SystemCallFilter=@system-service`` and ``~@privileged``
     - Controller only: a system call allow-list without privileged calls.
   * - ``UMask=0077``
     - Files are private to ``damson``.
   * - ``StateDirectory``
     - ``/var/lib/damson`` (controller) and ``/var/lib/damson-agent``
       (agent, also its working directory and ``HOME``), mode ``0750``.

``systemd-analyze security`` reports an exposure of 1.3 for the controller and
3.1 for the agent. The agent keeps the ability to start any process and has no
system call filter because it runs job code (``ssh``, ``ansible``, ``git``,
``helm``). ``make systemd-check`` verifies the units with
``systemd-analyze verify`` and fails when a core hardening line is removed.

Loosen the agent
~~~~~~~~~~~~~~~~

Loosen only what a job needs, with a drop-in
(``systemctl edit damson-agent``), never by editing the unit:

.. code-block:: ini

   [Service]
   # A path a job writes to (for example an ansible inventory cache).
   ReadWritePaths=/srv/inventory
   # A tool that needs more system calls.
   SystemCallFilter=@system-service
   # Jobs that read ~/.ssh or ~/.kube of the damson user.
   ProtectHome=no

Logs, stop and secrets
----------------------

- **Logs.** Both services write JSON ``slog`` lines to journald:
  ``journalctl -u damson-controller -o cat``.
- **Stop.** The controller stops its listeners and flushes logs within 15
  seconds; ``TimeoutStopSec=30s`` leaves room. The agent drains for
  ``agent.drain_grace`` (default 20 s), then cancels its running steps and
  gives them 5 seconds. Keep the two below ``TimeoutStopSec`` and raise them
  together for longer steps.
- **Secrets.** Credentials live only in the ``EnvironmentFile`` (mode ``0600``,
  owned by root). Never put them in a unit file, a drop-in or on a command
  line, where ``systemctl show`` or ``ps`` would reveal them.

Upgrade
-------

#. Take a database backup. Migrations run at start and are not reversed.
#. Replace ``/usr/local/bin/damson`` on one host.
#. ``systemctl restart damson-controller`` on that host (migrations run at
   start under the advisory lock), wait until it is healthy, then continue with
   the next controller. Migrations must be tolerated by the controllers that
   still run the old version (expand/contract).
#. Replace the binary on the agent hosts and restart ``damson-agent`` when no
   steps run on them.
