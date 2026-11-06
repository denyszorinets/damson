winrm plugin
============

The ``winrm`` plugin runs a command or a PowerShell script on one Windows
inventory node over WinRM. It is a built-in plugin in ``plugins/winrm``, written
against the public plugin SDK only (:doc:`/development/plugin-sdk`). Design:
ARCHITECTURE section 6.2 and
:doc:`/architecture/decisions/0049-winrm-transport-security-and-remote-execution`.

.. warning::

   The bootstrap and its behavior (stdin delivery to ``powershell.exe``, exit
   codes, no CLIXML on stderr, UTF-8 output) are covered by tests only against
   a fake WinRM server that interprets the payload as a mini script. They must
   be proven on a Windows host (a manual or nightly VM job, ARCHITECTURE
   section 19) before you rely on them.

.. list-table::
   :widths: 25 75

   * - Manifest name
     - ``winrm``
   * - Target mode
     - ``single_node``
   * - Idempotent
     - no
   * - Requires
     - nothing on the agent; Windows PowerShell on the node

Input
-----

.. list-table::
   :header-rows: 1
   :widths: 22 15 63

   * - Field
     - Type
     - Meaning
   * - ``node``
     - string, required
     - Name of the inventory node (transport ``winrm``).
   * - ``command``
     - string
     - Command line run through ``cmd.exe /d /s /c``. Exactly one of
       ``command`` and ``powershell``.
   * - ``powershell``
     - string
     - PowerShell script, run as a script block.
   * - ``env``
     - map of string
     - Environment variables. A value ``secret://...`` is resolved from the
       secret bundle.
   * - ``credential``
     - string
     - ``secret://`` ref that overrides the node's credential.
   * - ``transport``
     - ``https`` or ``http``
     - Default ``https``. See Rules.
   * - ``auth``
     - ``ntlm`` or ``basic``
     - Default ``ntlm``.
   * - ``insecure_skip_verify``
     - bool
     - Do not verify the server certificate. See Rules.
   * - ``success_exit_codes``
     - list of int
     - Exit codes that count as success, default ``[0]``.

Unknown fields are rejected.

Output
------

The same as :doc:`/reference/plugin-ssh`: ``exit_code`` (``-1`` if the command
did not exit normally), ``stdout_tail`` and ``stderr_tail`` (last 64 KiB, without
output-protocol lines) and ``values``. A failed step keeps its tails.

Credential
----------

The credential is resolved with ``ResolveFields`` and needs the fields
``username`` and ``password``. The username falls back to the node attribute
``username``. For NTLM it may be ``DOMAIN\user`` or ``user@domain``. Values are
never logged. Kerberos and CredSSP are not supported (post-v1, ADR-0025).

The port is the node's own port. The inventory default for a ``winrm`` node is
5986, so a node that is used with ``transport: http`` must store 5985.

Rules
-----

- ``auth`` defaults to ``ntlm``.
- ``basic`` over ``http`` is always refused, because the password would be sent
  in clear text.
- ``http`` needs ``auth: ntlm`` (the library transport then encrypts the messages
  with NTLM sealing) and the agent setting ``agent.allow_insecure_winrm``
  (``DAMSON_AGENT__ALLOW_INSECURE_WINRM``, default ``false``). The server is not
  authenticated; a warning is written to the system log on each use.
- ``insecure_skip_verify`` needs the same agent setting and is invalid over
  ``http``. For a private CA put the CA in ``agent.tls_ca_file`` instead.
- The node's ``host_key_policy`` and ``host_key`` are ignored: v1 has no
  certificate pinning. The controller still refuses policy ``insecure`` at
  dispatch.
- https verifies the certificate against the system roots plus
  ``agent.tls_ca_file``. The plugin owns its https transport so that the system
  roots stay trusted. TLS 1.2 is the minimum; no proxy is taken from the
  environment.

Execution
---------

The only thing on the command line is a fixed bootstrap::

   powershell.exe -NoLogo -NoProfile -NonInteractive -OutputFormat Text -EncodedCommand <base64 UTF-16LE>

The payload (script, ``env`` values, resolved secrets) is sent as base64 JSON on
standard input. It therefore never appears in the process command line, in
process creation event 4688 or in the 8191-character command line limit.
PowerShell script block logging (event 4104) records the bootstrap and the
user's script text, but not ``env`` values.

``powershell`` runs as a script block; ``command`` runs through ``cmd.exe``
with ``/d /s /c``. ``ConvertFrom-Json`` in Windows PowerShell 5.1 limits the
payload to about 2 MB.

Exit code:

- an explicit ``exit N`` gives ``N``;
- a terminating error gives ``1`` and its text goes to stderr;
- otherwise the exit code of the last native command (``$LASTEXITCODE``);
- for ``command``, the exit code of ``cmd.exe``.

An exit code outside ``success_exit_codes`` is an ``exit_code`` error. Lines
starting with ``##damson-output`` on standard output are handled and stripped
as in the ``ssh`` plugin (shared code in ``plugins/internal/scriptout``).

Limits and notes
----------------

- ``command`` must be one line: ``cmd.exe`` runs only the first line of a
  multi-line text. Use ``powershell`` for a script.
- Environment names that differ only in case (``Path`` and ``PATH``) are
  refused; Windows treats them as one variable. An empty value removes the
  variable from the process environment.
- The output of a ``command`` is in the console's OEM code page, not UTF-8
  (PowerShell output is UTF-8). Set ``chcp 65001`` in the command if you need
  UTF-8 from ``cmd.exe`` programs.
- All requests of a step share one connection with NTLM, so a cancel signal
  queues behind a running output poll (up to 5 seconds). The step still returns
  within about 2 seconds; the terminate signal and the shell delete then
  complete in the background and can reach the node a few seconds later.

Cancel
------

On cancel or timeout the plugin sends the terminate signal, then deletes the
shell, which ends the process tree on the node. It returns within about 2
seconds. The shell is always deleted. A step in ``http`` mode leaves its idle
connection to be closed by the node's idle timeout (a limit of the library).
In ``http`` mode the library's transport also takes a proxy from the
``HTTP_PROXY`` and ``NO_PROXY`` environment variables of the agent, which it
cannot turn off, and the sealed payload requires a 128-bit NTLM key. The
``https`` transport never uses a proxy.

A cancelled step stops collecting output at once; lines that still arrive from
the node are dropped.

Errors
------

.. list-table::
   :header-rows: 1
   :widths: 25 75

   * - Kind
     - When
   * - ``auth``
     - No credential, missing username or password, HTTP 401, access denied.
   * - ``connection``
     - Network failure, TLS or certificate verification failure.
   * - ``validation``
     - Bad input, unknown node or wrong transport, ``http`` or
       ``insecure_skip_verify`` without ``agent.allow_insecure_winrm``, ``basic``
       over ``http``.
   * - ``exit_code``
     - Exit code outside ``success_exit_codes``.

Testing
-------

``plugins/winrm`` is tested against ``plugins/internal/winrmtest``, an
in-process fake WinRM server. See the warning at the top: this does not replace
a run against a real Windows host.
