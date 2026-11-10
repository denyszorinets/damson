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
  certificate validation; ``agent.tls_ca_file`` is the trust path (it replaces
  the system roots for these hosts, unlike the ``winrm`` plugin). They need the
  ``pywinrm`` Python package on the agent.
* Any other transport is a ``validation`` error, as is a node that has no
  credential and no ``credential`` default (``auth``).

Every file with a secret (inventory, keys, extra variables, vault password,
known_hosts, the callback's events) lives in ``WorkDir/creds`` with mode 0600 and
is removed when the step ends. Nothing secret is in the argument list, and
ansible gets a minimal environment, not the agent's own. ``creds`` is under the
work directory, not on a tmpfs; give the agent a work directory on a tmpfs if
that matters.

**No templating of untrusted strings.** Every string in the inventory (node
attributes, the user name, passwords) and every string in ``extra_vars`` is
marked unsafe, so ansible never runs it through Jinja2. A CMDB-synced attribute,
a run parameter or a secret that contains ``{{ lookup(...) }}`` is data, not a
template, and cannot run anything on the agent. The playbook itself still
templates its own text. If you relied on a templated value in ``extra_vars``,
do the templating in the playbook.

**ssh identity.** Each ssh host gets ``-F /dev/null``, ``IdentitiesOnly``,
``IdentityAgent=none``, no GSSAPI or host-based auth and ``BatchMode``; a
password credential also gets ``PubkeyAuthentication=no``. The agent user's own
``~/.ssh`` config, keys and agent are never used. ``ANSIBLE_SSH_ARGS`` is set in
the environment, so a repository ``ansible.cfg`` cannot re-enable ssh
connection sharing (a detached master would outlive a cancel), and
``ANSIBLE_LOG_PATH`` is ``/dev/null``. A ``ansible_ssh_common_args`` from
``group_vars`` (for a ProxyJump bastion) is overridden by the generated one;
configure a bastion in the playbook's own ``ansible_ssh_extra_args`` instead.

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

   A playbook, its ``ansible.cfg``, ``host_vars`` and play variables are code
   that runs on the agent under the agent's user. They outrank the inventory, so
   they can turn host key checking off (``ansible_ssh_common_args``,
   ``ansible_host_key_checking``, ``ssh_args``), read the credential files and
   other steps' files in the work directory tree, and read the agent's own
   process environment (its pool token). The host key check protects against
   network interception, not against a hostile playbook. Only run playbooks you
   trust, and run ansible steps in a dedicated agent pool.

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
process group (ssh connection sharing is switched off, so no ssh master survives).

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
