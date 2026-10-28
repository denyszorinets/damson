Agents and plugins
==================

Agents and pools
----------------

An **agent** is a stateless worker process (``damson agent``) that connects
out to the controllers over ``wss://`` and executes steps. Agents are grouped
into **pools**, typically one per network zone (``dc1``, ``dc2``, ``k8s``), and
reach target nodes over SSH, WinRM, SFTP, FTP or S3. Targets run no Damson
software.

Each agent advertises:

- its pool and labels;
- capacity (``max_concurrent``, default CPU count × 4);
- capabilities, such as ``bin:ansible-playbook`` or ``bin:git``, detected at
  startup;
- the manifests of every plugin compiled into its binary.

A step runs on an agent from the pool chosen by ``runs_on`` or the target
node's pool, that has the plugin at a compatible version and free capacity.

If a controller goes away, agents keep executing and reconnect to another one.
If an agent goes away, its executions are marked ``lost`` when their lease
expires and are retried when the plugin is idempotent or the step allows it.

Plugins
-------

A **plugin** implements one kind of step. Its input and output are Go structs;
JSON Schemas are generated from them and drive validation and the UI forms.

Built-in plugins (v1):

.. list-table::
   :header-rows: 1
   :widths: 15 85

   * - Plugin
     - Purpose
   * - ``ssh``
     - Run a command or script on a node; host key policies
       ``strict``/``tofu``/``insecure`` (:doc:`/reference/plugin-ssh`).
   * - ``winrm``
     - Run a command or PowerShell on Windows (HTTPS, Basic or NTLM).
   * - ``transfer``
     - Copy or move one file between nodes (SFTP, SCP, FTP, FTPS) and S3,
       streamed through agent memory with in-stream SHA-256.
   * - ``ansible``
     - Run a playbook from Git with an inventory generated from the run's
       targets.
   * - ``buildah``
     - Build and push a container image from Git, on dedicated build pools.
   * - ``helm``
     - Upgrade, roll back, uninstall or inspect a Helm release.

Plugins are compiled into the binary. Built-ins use the same public SDK
(``pkg/pluginsdk``) that third-party plugins use; custom binaries with extra
plugins are built with ``damson build --with <module>@<version>``
(:doc:`/architecture/decisions/0014-compiled-in-plugins-with-a-public-sdk`).

Scripts run by ``ssh`` and ``winrm`` can return values by printing
``##damson-output key=value`` or ``##damson-output-json {...}``.

.. _plugin-registry:

Plugin registry
---------------

The controller keeps a plugin registry (``internal/pluginreg``). It holds the
manifests of the built-in plugins, which the controller links itself, so their
schemas are known with no agent connected, and the manifests every connected
agent sent in ``agent.hello``. The registry is a derived cache, not durable
state: the gateway adds an agent's manifests after a successful hello and drops
them when that agent's socket ends, and a restarted controller rebuilds it as
agents reconnect. It therefore describes the agents connected to *this*
controller. ARCHITECTURE section 6 also names a ``plugin_manifests`` table; it
is deferred until several controllers must share one view (M3, HA), because
nothing before then needs a manifest the controller has not seen live.

* **Deduplication.** Manifests are keyed by name, version and checksum; many
  agents reporting the same manifest are one entry with many providers.
* **Resolution.** ``plugin`` picks the highest semver version that at least
  one eligible agent can run. ``plugin@1`` pins the major version. An agent is
  eligible when it is in the requested pool, reports the accepted manifest and
  has every capability in the manifest's ``requires``.
* **Conflicts.** If agents report different checksums for one name and
  version, the controller's own manifest wins, otherwise the most reported one
  (ties go to the smaller checksum). Agents on the other side are listed under
  ``conflicts`` in the API and are never chosen for dispatch.
* **Input validation.** Before dispatch the resolved step input is validated
  against the manifest ``input_schema`` (the agent's own ``Validate`` still
  runs later). Errors name the field path and the rule, never the value, and a
  ``secret://`` reference is an ordinary string.
