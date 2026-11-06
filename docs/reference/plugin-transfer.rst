transfer plugin
===============

The ``transfer`` plugin copies or moves one file between two inventory nodes.
It is a built-in plugin in ``plugins/transfer``, written against the public
plugin SDK only (:doc:`/development/plugin-sdk`). Design: ARCHITECTURE
section 6.3.

.. note::

   Only the ``sftp`` protocol and node endpoints exist so far. ``scp`` (#145),
   ``ftp`` and ``ftps`` (#146) and S3 endpoints (#147) are planned and are not
   accepted yet: any other protocol is a validation error.

.. list-table::
   :widths: 25 75

   * - Manifest name
     - ``transfer``
   * - Target mode
     - ``nodes``, with node paths ``/source/node/name`` and
       ``/destination/node/name`` (:doc:`/architecture/decisions/0048-plugins-that-target-several-nodes`)
   * - Idempotent
     - no (a lost attempt may still run on a disconnected agent, and a retried
       move finds its source gone)
   * - Requires
     - nothing on the agent; the nodes need an SFTP subsystem

Input
-----

.. list-table::
   :header-rows: 1
   :widths: 20 15 65

   * - Field
     - Type
     - Meaning
   * - ``mode``
     - string, required
     - ``copy`` leaves the source in place; ``move`` deletes it after the copy
       was verified.
   * - ``source``
     - endpoint, required
     - ``{"node": {"name", "protocol", "path"}}``. ``name`` is a node of the
       step, ``protocol`` is ``sftp``, ``path`` is an absolute path of a file.
       A path ending in ``/`` is a validation error.
   * - ``destination``
     - endpoint, required
     - Same shape. A path ending in ``/`` is a directory and gets the source
       file name appended.
   * - ``verify``
     - string
     - ``none``, ``size`` (default) or ``sha256``. See below.
   * - ``overwrite``
     - bool
     - Replace an existing destination. Default ``false``: an existing
       destination fails the step.
   * - ``mkdirs``
     - bool
     - Create missing parent directories of the destination.

Unknown fields are rejected. Paths must be absolute and single-line.

Output
------

.. list-table::
   :header-rows: 1
   :widths: 25 75

   * - Field
     - Meaning
   * - ``destination_path``
     - Path written on the destination node (after the trailing-slash rule).
   * - ``bytes``
     - Bytes transferred.
   * - ``sha256``
     - Hex SHA-256 of the transferred bytes, computed in-stream.

The outputs stay set when a ``move`` fails only at the last step (see below).

Behavior
--------

A step transfers exactly one file. The data is streamed from the source node
through agent memory to the destination node with the SHA-256 computed
in-stream; nothing is written to the agent's disk. Progress is reported about
once a second.

1. The source is checked (it must exist and be a file) and the destination is
   checked (not a directory; with ``overwrite: false`` it must not exist).
   With ``mkdirs`` the parent directory is created.
2. The file is written to ``<path>.damson-partial-<8 hex>``. The random suffix
   keeps a lost attempt and its retry from writing the same file.
3. The partial file is verified (below).
4. The partial file is renamed to the destination. With ``overwrite: false``
   this is a plain SFTP rename, which fails if the target exists, so a file
   created after the check is not replaced. With ``overwrite: true`` it is a
   ``posix-rename``.
5. For ``move`` the source is deleted.

A failed or cancelled transfer never exposes a partial destination. The partial
file is deleted best effort on failure or cancel; after a cancel the plugin
connects again to do so.

Verification
~~~~~~~~~~~~

Verification runs on the partial file, before the rename.

``none``
   No check.

``size``
   The source size must equal the bytes streamed and the size of the written
   file.

``sha256``
   As ``size``, and the written file is read back and its digest compared with
   the in-stream digest. This doubles the reads on the destination.

A failure is an ``internal`` error whose message starts with
``verification failed:``; the destination is left untouched.

Move
~~~~

``move`` is copy, verify, then delete the source. If deleting the source fails
the step fails with ``copied and verified; source not deleted`` while the
outputs stay set. The destination is never deleted. A move retried after the
source is gone fails with ``source not found``.

Connections and host keys
-------------------------

Both endpoints connect over SSH with the same code as the ``ssh`` plugin
(``plugins/internal/sshconn``): the node's ``host_key_policy``
(``strict``, ``tofu``, ``insecure``), trust-on-first-use recording and
credential resolution behave exactly as described in
:doc:`/reference/plugin-ssh`. A TOFU key is reported per node, once
authentication succeeded. The agent setting ``allow_insecure_host_keys`` also
applies to ``transfer``. A node must have transport ``ssh``.

Failure handling
----------------

.. list-table::
   :header-rows: 1
   :widths: 40 60

   * - Situation
     - Result
   * - Invalid input, unknown node, destination exists, source not found,
       source or destination is a directory, wrong transport
     - ``validation`` error
   * - Connection or SFTP failure, host key mismatch
     - ``connection`` error
   * - No credential, bad key, login rejected
     - ``auth`` error
   * - Context cancelled
     - ``cancelled``; blocked transfers are aborted by closing the connections
   * - Verification failed
     - ``internal`` error, ``verification failed: ...``
   * - Source not deleted after a move
     - ``copied and verified; source not deleted``, outputs set

Whether a failure is retried is decided by the step's retry rules, not by the
plugin.
