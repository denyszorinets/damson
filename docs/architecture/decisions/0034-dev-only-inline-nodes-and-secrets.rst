ADR-0034: Dev-only inline nodes and secrets (--dev-nodes) until inventory and secrets land in M4
================================================================================================

:Status: Accepted
:Date: 2026-10-29

Context
-------

Milestone M2 needs an end-to-end ``ssh`` run from ``damson dev``. The inventory
API, the secret store and node delivery belong to M4, but the ``step.execute``
message already has ``Nodes`` and ``Secrets`` fields
(:doc:`/reference/protocol`). Without something that fills them, no plugin that
targets a node or uses a ``secret://`` reference can run.

Decision
--------

- ``dispatch.Config`` has optional ``NodeSource`` and ``SecretSource``. When
  they are set, ``buildRequest`` fills ``step.execute``:

  - ``Nodes`` for plugins whose ``TargetMode`` is ``single_node``, taken from
    the input field ``node``.
  - ``Secrets`` with every ``secret://`` string in the input plus the node
    credentials, keyed by the base ref without ``#field``, at most 64 refs.

  The secret values are wiped after the send. With no sources configured
  nothing changes.
- The only implementation is ``internal/devnodes``, loaded from a JSON file
  named by the CLI flag ``--dev-nodes``. It is a flag only: there is no config
  key and no environment variable.
- The flag is accepted on ``dev``, and on ``controller`` only together with
  ``--insecure-dev`` (needed for the Postgres plus separate agent test). It is
  rejected on ``agent``.
- File format: ``version`` 1; ``nodes`` maps a name to ``address``, ``port``
  (default 22), ``transport``, ``os``, ``attributes``, ``credential``,
  ``host_key`` and ``host_key_policy`` (``strict``, ``tofu`` or ``insecure``);
  ``secrets`` maps ``secret://ref`` to ``username`` (inline allowed) and
  ``<field>_file`` (absolute path). Unknown fields are rejected.
- Secret values come only from files: mode 0600 or stricter, a regular file, at
  most 64 KiB. They are read at every dispatch and never cached.
- Startup logs a WARN naming the file path and the node names.
- An unknown node or secret fails the execution permanently (compare-and-swap,
  ``run.advance`` enqueued, the message names only the node or ref). Other
  source errors requeue the execution.

Consequences
------------

Positive
~~~~~~~~

- M2 has a real end-to-end ``ssh`` run, tested against an sshd container.
- The dispatcher, the refs and the ``step.execute`` shape are the ones M4 uses.

Negative
~~~~~~~~

Accepted M2 gaps:

- No project-ancestor authorization (ARCHITECTURE §9.1) on dev refs.
- The controller does not re-mask on ingest (§9.4); the agent masks, including
  the username.
- No handler stores ``tofu`` host keys; use ``strict`` with a pinned
  ``host_key``.

Follow-up
~~~~~~~~~

- M4 replaces ``internal/devnodes`` with inventory and ``SecretStore`` backed
  sources. Whether ``devnodes`` is then removed or becomes a seeder is open.

References
----------

- :doc:`/reference/cli`
- :doc:`/tutorial/ssh-job`
- :doc:`0015-secret-references-and-envelope-encryption`
