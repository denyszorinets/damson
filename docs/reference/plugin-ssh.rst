ssh plugin
==========

The ``ssh`` plugin runs a command or a script on one inventory node over SSH.
It is a built-in plugin in ``plugins/ssh``, written against the public plugin
SDK only (:doc:`/development/plugin-sdk`). Design: ARCHITECTURE section 6.1.

.. list-table::
   :widths: 25 75

   * - Manifest name
     - ``ssh``
   * - Target mode
     - ``single_node``
   * - Idempotent
     - no (a lost execution is not retried automatically)
   * - Requires
     - nothing on the agent

Input
-----

.. list-table::
   :header-rows: 1
   :widths: 20 15 65

   * - Field
     - Type
     - Meaning
   * - ``node``
     - string, required
     - Name of the inventory node (a key of the step's nodes).
   * - ``command``
     - string
     - Command line, run as ``<interpreter> -c <command>``. Exactly one of
       ``command`` and ``script``.
   * - ``script``
     - string
     - Script body. It is written to a temporary file on the node (``mktemp``
       in ``$TMPDIR`` or ``/tmp``), run as ``<interpreter> <file>`` and removed
       afterwards.
   * - ``interpreter``
     - string
     - Interpreter and optional arguments, default ``/bin/sh``. It must accept
       ``-c`` for ``command``.
   * - ``env``
     - map of string
     - Environment variables for the command. A value of the form
       ``secret://...`` is resolved from the secret bundle.
   * - ``workdir``
     - string
     - Remote working directory.
   * - ``sudo``, ``sudo_user``
     - bool, string
     - Run through ``sudo -n [-u <user>]``. Passwordless sudo is required;
       ``sudo_user`` needs ``sudo``.
   * - ``pty``
     - bool
     - Request a pseudo terminal. stderr is then merged into stdout by the
       terminal. See the note on process lists below.
   * - ``credential``
     - string
     - ``secret://`` ref of an ``ssh_key`` credential. Overrides the node's
       own credential.
   * - ``success_exit_codes``
     - list of int
     - Exit codes that count as success, default ``[0]``. Each is 0 to 255.

Unknown fields are rejected. Cross-field rules (command XOR script,
``sudo_user`` requires ``sudo``, valid ``env`` names) are checked by the
plugin's ``Validate`` on the agent.

The command, the script and resolved ``env`` values are sent to the node on the
standard input of a fixed ``/bin/sh -s`` command, so they do not appear in the
remote process list. With ``pty: true`` standard input cannot be used and the
whole body, including the command or script text, is passed as an argument of
``/bin/sh -c`` and **is visible in the remote process list**. Because env
values (which may be secrets) would be exposed that way, ``pty`` together with a
non-empty ``env`` is rejected by validation; use a step without ``pty`` when you
need ``env``.

In script mode the interpreter is not ``exec``-ed, so the shell can remove the
temporary file when the interpreter exits. If the connection is lost or the
shell is killed hard, the file under ``$TMPDIR`` (or ``/tmp``) can remain.

Credential
----------

The credential is resolved with ``ResolveFields`` and may hold ``username``
(falls back to the node attribute ``username``), ``private_key``,
``passphrase`` and ``password``. The key is tried first when both are present.
Only public key and password authentication are offered; keyboard-interactive
is not. Credential values are never logged and never appear in outputs or error
messages. What the remote command itself prints (for example ``echo $TOKEN``)
is not filtered by the plugin: masking of such output is done by the agent
masker (ARCHITECTURE section 9.4), which the ``plugintest`` fake emitter does
not apply, so it is not covered by this package's tests.

Host keys
---------

The node's ``host_key_policy`` decides how the server key is checked:

``strict`` (default)
   The node must have a ``host_key`` (authorized_keys format). Any other key
   fails the step with a ``connection`` error. A missing ``host_key`` is a
   ``validation`` error.

``tofu``
   If the node has a ``host_key`` it is enforced like ``strict``. Otherwise
   the key the server presents is accepted and, once authentication succeeded,
   reported through the SDK hook (``ExecRequest.HostKeys``). The agent forwards
   it to the controller, which stores it as ``hostkey_observed`` on the node
   for an operator to review. The plugin side is done; the agent wiring of
   ``HostKeys`` and ``Config`` is pending (tracked in #52), so until then the
   hook is nil and ``insecure`` cannot be enabled outside tests.

``insecure``
   No verification. It is refused with a ``validation`` error unless the
   operator enabled it: the agent must pass the plugin setting
   ``allow_insecure_host_keys`` = ``true`` in ``ExecRequest.Config``. A system
   log line warns on every use.

Output
------

.. list-table::
   :header-rows: 1
   :widths: 20 80

   * - Field
     - Meaning
   * - ``exit_code``
     - Exit code of the remote command; ``-1`` if it was killed by a signal
       or did not finish.
   * - ``stdout_tail``, ``stderr_tail``
     - The last 64 KiB of each stream (without output-protocol lines).
   * - ``values``
     - Values set by the script (below). Absent when none were set.

Output and logs are produced even when the step fails, so a failed step keeps
its tails.

Script output protocol
~~~~~~~~~~~~~~~~~~~~~~

Lines on standard output that start with ``##damson-output key=value`` set one
string value; ``##damson-output-json {"k": 1}`` merges a JSON object. Both are
stripped from the log stream and the tail. A malformed protocol line is dropped
and a system log warning is written (without repeating the line). Standard
error is never parsed.

Streaming and failure handling
------------------------------

stdout and stderr are streamed to the run log line by line while the command
runs. A line longer than 64 KiB is split.

.. list-table::
   :header-rows: 1
   :widths: 40 60

   * - Situation
     - Result
   * - Exit code outside ``success_exit_codes``
     - ``exit_code`` error carrying the code
   * - Killed by a signal
     - ``exit_code`` error, ``exit_code`` output ``-1``
   * - Connection refused, handshake failure, host key mismatch
     - ``connection`` error
   * - No credential, bad key or passphrase, server rejects the login
     - ``auth`` error
   * - Invalid input, unknown node, ``strict`` without ``host_key``,
       ``insecure`` not enabled
     - ``validation`` error
   * - Context cancelled
     - ``cancelled``; the plugin sends ``SIGTERM`` through the session, waits
       up to 2 seconds for the command to exit, then closes the session and the
       connection
   * - Context deadline (step timeout)
     - ``timeout``, handled like a cancel

Whether a failure is retried is decided by the step's retry rules, not by the
plugin.

Testing
-------

``plugins/ssh`` is covered by unit tests that run against an in-process SSH
server (``plugins/internal/sshtest``) executing commands with the local
``/bin/sh``, and by integration tests against an OpenSSH container:

.. code-block:: console

   $ go test -race ./plugins/ssh/...
   $ make test-integration                      # needs Docker

The integration tests use ``testcontainers-go`` with the
``linuxserver/openssh-server`` image and cover success, a non-zero exit,
cancel, timeout, a strict host key mismatch and the trust-on-first-use report.
