ansible plugin
==============

The ``ansible`` plugin clones a git repository and runs one playbook over a list
of inventory nodes with ``ansible-playbook``. It is a built-in plugin in
``plugins/ansible``, written against the public plugin SDK only
(:doc:`/development/plugin-sdk`). Design: ARCHITECTURE section 6.4 and
:doc:`/architecture/decisions/0055-inventory-target-mode`.

.. list-table::
   :widths: 25 75

   * - Manifest name
     - ``ansible``
   * - Target mode
     - ``inventory``, node list at ``/hosts``
   * - Requires
     - ``bin:ansible-playbook``, ``bin:git``
   * - Idempotent
     - no

Input
-----

.. list-table::
   :widths: 25 75
   :header-rows: 1

   * - Field
     - Meaning
   * - ``repo.url``, ``repo.ref``
     - Repository (``https`` or ``ssh``) and a branch, tag or full commit SHA.
       Empty ``ref`` is the default branch.
   * - ``repo.credential``
     - A ``token`` secret (https) or an ``ssh_key`` secret (ssh). It is optional
       for public repositories.
   * - ``repo.submodules``
     - Also clone submodules.
   * - ``playbook``
     - Relative path of the playbook in the repository.
   * - ``requirements``
     - Relative path of a ``requirements.yml``; roles and collections are
       installed with ``ansible-galaxy install -r`` before the run.
   * - ``hosts``
     - **Required.** The names of the inventory nodes to run on, normally
       ``${{ targets.map(t, t.name) }}``. At most 1000. An empty list succeeds
       without running anything.
   * - ``extra_vars``
     - Extra variables. A string value ``secret://...`` is resolved from the
       secret bundle.
   * - ``limit``, ``tags``, ``skip_tags``, ``check``, ``diff``, ``forks``, ``verbosity``
     - The ansible options of the same name. ``limit`` can only narrow ``hosts``.
   * - ``vault_password``
     - ``secret://`` ref of the Ansible Vault password.
   * - ``credential``
     - Credential for hosts whose node has none.

Hosts, groups and credentials
-----------------------------

The controller sends every listed node with its credential and tags (a name it
cannot use fails the whole step). The plugin writes the inventory as a JSON
document in the YAML inventory format: one host per node with ``ansible_host``,
``ansible_port`` and ``ansible_user``, the node attributes (scalar values whose
names do not start with ``ansible_``) as variables, and one group per node tag
(characters outside ``[A-Za-z0-9_]`` become ``_``; a leading digit gets a
``tag_`` prefix).

* ``ssh`` nodes use ``ansible_connection=ssh`` with the credential's
  ``private_key`` (written to a 0600 file) or ``password`` (needs ``sshpass`` on
  the agent). A passphrase-protected key is refused. ``become_password`` in the
  credential sets ``ansible_become_password``.
* ``winrm`` nodes use ``ansible_connection=winrm`` over https with NTLM and
  certificate validation; ``agent.tls_ca_file`` is the trust path. They need the
  ``pywinrm`` Python package on the agent.
* Any other transport is a ``validation`` error, as is a node that has no
  credential and no ``credential`` default (``auth``).

Every file with a secret (inventory, keys, extra variables, vault password,
known_hosts) lives in ``WorkDir/creds`` with mode 0600 and is removed when the
step ends. Nothing secret is in the argument list or the environment.

Host keys
---------

Each ssh host gets ``-o HostKeyAlias=<node name>``, so a key belongs to the
node, not to an address, and a private ``UserKnownHostsFile``. ``strict`` nodes
use ``StrictHostKeyChecking=yes`` with the pinned ``host_key`` (without one the
host is unreachable). ``tofu`` nodes use ``yes`` with a key and ``accept-new``
without one; after the run, also after a failure or a cancel, the plugin reports
the keys ssh recorded for such nodes and the controller stores the first one
(ADR-0046). ``ANSIBLE_HOST_KEY_CHECKING=True`` is forced in the environment. A
node with policy ``insecure`` needs ``agent.allow_insecure_host_keys``.

.. warning::

   Play variables outrank inventory variables, so a playbook can set
   ``ansible_ssh_common_args`` or ``ansible_host_key_checking`` and turn host key
   checking off. The check protects against network interception, not against a
   hostile playbook, which can already read the credential files with
   ``delegate_to: localhost``. Only run playbooks you trust.

Output
------

``stats`` (per host: ``ok``, ``changed``, ``failed``, ``unreachable``,
``skipped``, from the callback plugin ``damson_events``), ``failed_hosts``
(failed or unreachable), ``changed_hosts``, ``commit`` (the resolved SHA) and
``exit_code``. A failed step still returns these.

Errors: unreachable hosts only are ``connection``; failed tasks are
``exit_code``; a missing node or credential and bad input are ``validation`` or
``auth`` and fail before anything is cloned. Output of ``ansible-playbook`` is
streamed to the log (``stdout`` and ``stderr``). Cancelling kills the whole
process group.

Agent settings
--------------

``agent.allow_insecure_host_keys`` (policy ``insecure`` nodes and ssh
repositories without known_hosts), ``agent.allow_insecure_git`` (``http://``,
``git://`` and ``file://`` repository URLs, off by default) and
``agent.tls_ca_file`` (winrm). A Galaxy mirror is configured with the agent
process's ``ANSIBLE_GALAXY_*`` environment variables, which are passed on.

Git checkout
------------

The clone uses the shared helper described in :doc:`/development/plugin-sdk`:
depth 1, no hooks, a token only for the repository's own host, and no use of the
agent user's ssh keys or config.
