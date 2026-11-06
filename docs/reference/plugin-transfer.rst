transfer plugin
===============

The ``transfer`` plugin copies or moves one file between two inventory nodes.
It is a built-in plugin in ``plugins/transfer``, written against the public
plugin SDK only (:doc:`/development/plugin-sdk`). Design: ARCHITECTURE
section 6.3.

.. note::

   Node endpoints with protocol ``sftp``, ``scp``, ``ftp`` or ``ftps`` exist.
   S3 endpoints (#147) are planned and are not accepted yet: any other
   protocol is a validation error.

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
     - nothing on the agent; ``sftp`` nodes need an SFTP subsystem, ``scp`` nodes
       a POSIX shell and the ``scp`` binary (see SCP)

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
       step, ``protocol`` is ``sftp``, ``scp``, ``ftp`` or ``ftps``, ``path`` is an absolute path of a file.
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
   ``posix-rename``. SCP destinations differ, see below.
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

SCP
---

``protocol: scp`` is accepted on the source and the destination, and the two
may be mixed (``scp`` to ``sftp`` and the reverse). No extra dependency is
needed: the SCP wire protocol is implemented over an SSH session (``scp -f``
to read, ``scp -t`` to write). Only a single regular file is transferred, and
it is created with mode ``0644``, subject to the remote umask.

Stat, ``mkdirs``, the final rename and the removal after a ``move`` run as
POSIX shell commands through ``/bin/sh -c`` (``[ -d ]``, ``wc -c``,
``mkdir -p``, ``mv``, ``rm -f``). The node therefore needs a POSIX shell and
the ``scp`` binary. Windows targets must use SFTP; a node with the ``winrm``
transport is refused.

SCP needs the file size before the data is sent. It is taken from the source
stat, so a source that changes size during the copy fails (``more data than
the announced size`` or a short write).

With ``overwrite: false`` the destination is checked by stat and again with
``[ -e ]`` just before the rename. Unlike SFTP there is no atomic no-clobber
rename, so a writer that creates the destination in between wins: the check is
best effort. With ``overwrite: true`` the rename is ``mv -f``.

The partial file name, verification on the partial file, ``move``, TOFU host
keys and cleanup on cancel are the same as for ``sftp``.

FTP and FTPS
------------

``protocol: ftp`` and ``protocol: ftps`` are accepted on the source and the
destination and may be mixed with ``sftp`` and ``scp`` across nodes. The client
library is ``github.com/jlaffaye/ftp`` (ISC). Passive mode only; active mode is
not used.

.. warning::

   Plain ``ftp`` sends the password and the data in clear text. Use ``ftps``.
   Every plain ``ftp`` endpoint writes a warning to the step log.

``ftps`` never falls back to clear text: a server that does not accept ``AUTH
TLS``, or that rejects the protected data channel (``PBSZ``/``PROT P``), fails
the step as a ``connection`` error before any credential is sent. Only a
rejected ``USER``/``PASS`` (reply 530 or 430) is an ``auth`` error.

Servers that require the TLS session of the control connection to be reused
for data connections by session ID (for example vsftpd with
``require_ssl_reuse=YES``) can fail, because the client resumes sessions with
tickets only. Turn that option off for the transfer user.

.. list-table::
   :header-rows: 1
   :widths: 25 75

   * - Node attribute
     - Meaning
   * - ``ftp_port``
     - FTP port. Default ``21``, or ``990`` for implicit ``ftps``. The node's
       ``port`` is the SSH port and is not used.
   * - ``ftps_mode``
     - ``implicit`` selects implicit TLS. Otherwise ``ftps`` is explicit TLS
       (``AUTH TLS``, RFC 4217, TLS 1.2 or newer).
   * - ``username``
     - Optional; used when the credential has no ``username`` field.

The node credential needs ``username`` (or the node attribute ``username``)
and ``password`` fields. Anonymous login is not supported. The node transport
may be ``ssh`` or ``winrm``; FTP does not use it.

With ``ftps`` data connections are protected (``PROT P``). The server
certificate is always verified against the system roots plus the agent
setting ``agent.ftps_ca_file`` (``DAMSON_AGENT__FTPS_CA_FILE``), a PEM file of
extra CA certificates for a private PKI. It is read at agent start; a missing
or invalid file fails startup. There is no skip-verify option. The agent hands
the PEM to the plugin as ``ExecRequest.Config`` key ``ftps_ca_pem``.

FTP has no atomic no-clobber rename. With ``overwrite: false`` the destination
is checked first, which is best effort. With ``overwrite: true`` the file is
renamed, deleting the target first if the server refuses to rename onto it.
The source size is known up front, so a source that changes size during the
copy fails. The partial file name, verification, ``move`` and cleanup are the
same as for ``sftp``: on cancel the control and data connections are closed
and the partial file is removed over a new connection.

A certificate or TLS handshake problem is a ``connection`` error; a server
rejecting the login is an ``auth`` error. Tests run against an in-process
server (``github.com/fclairamb/ftpserverlib``, MIT), not containers.

Connections and host keys
-------------------------

``sftp`` and ``scp`` endpoints connect over SSH with the same code as the ``ssh`` plugin
(``plugins/internal/sshconn``): the node's ``host_key_policy``
(``strict``, ``tofu``, ``insecure``), trust-on-first-use recording and
credential resolution behave exactly as described in
:doc:`/reference/plugin-ssh`. A TOFU key is reported per node, once
authentication succeeded. The agent setting ``allow_insecure_host_keys`` also
applies to ``transfer``. A node of an ``sftp`` or ``scp`` endpoint must have transport ``ssh``.

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
