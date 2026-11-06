ADR-0049: WinRM transport security and remote execution
=======================================================

:Status: Accepted
:Date: 2026-11-06

Context
-------

The ``winrm`` plugin (ARCHITECTURE 6.2, :issue:`148`) runs scripts on Windows
nodes, usually with secrets in ``env``. WinRM over plain http, Basic
authentication and skipped certificate verification all weaken the connection,
and a Windows command line is visible in the process list, in event 4688 and has
an 8191-character limit. The ``masterzen/winrm`` library ignores ``ctx`` in its
HTTP calls and its https transport trusts only the extra CA when one is given.

Decision
--------

Operator opt-in
~~~~~~~~~~~~~~~

- New agent setting ``agent.allow_insecure_winrm`` (default false). It gates
  ``transport: http`` and ``insecure_skip_verify``.
- It does not reuse ``allow_insecure_host_keys``, which means trust in SSH host
  keys, not in WinRM transport security.
- ``basic`` over ``http`` is refused always, with or without the setting.
- ``http`` is allowed only with NTLM message encryption (sealing).
- ``insecure_skip_verify`` is invalid over ``http``.

Host keys
~~~~~~~~~

The node's ``host_key_policy`` and ``host_key`` are ignored for ``winrm``. There
is no certificate pinning in v1; it would need a schema field and is post-v1.
The controller still refuses policy ``insecure``.

Stdin bootstrap
~~~~~~~~~~~~~~~

The command line holds only a fixed ``powershell.exe ... -EncodedCommand``
bootstrap. The script, ``env`` values and secrets travel as base64 JSON on
stdin, so secrets never appear in argv. If delivering stdin to ``powershell.exe``
does not work on a given host, the plugin must refuse ``secret://`` ``env``
values rather than put them in argv. The behavior is tested only against a fake
server so far; a Windows host run is required (ARCHITECTURE 19).

Own https transport
~~~~~~~~~~~~~~~~~~~

The plugin uses its own https transport: system roots plus the extra CA, TLS 1.2
minimum, no proxy from the environment, and idle connections closed at the end
of a step. For context, the CA setting was renamed to ``agent.tls_ca_file`` in an
earlier change and applies to ``ftps``, https S3 and ``winrm``.

Cancel
~~~~~~

Library HTTP calls ignore ``ctx``, so they run in goroutines and the step returns
when ``ctx`` ends. Cancel sends the terminate signal and deletes the shell (which
ends the process tree), bounded to 2 seconds. An http-mode connection is left to
the node's idle timeout.

Shared collector
~~~~~~~~~~~~~~~~

The line collector for output-protocol lines and tails moved from ``plugins/ssh``
to ``plugins/internal/scriptout`` and is shared by ``ssh`` and ``winrm``.

Alternatives Considered
-----------------------

- ``env`` on the command line: visible in the process list, event 4688 and
  subject to the length limit.
- Reusing ``allow_insecure_host_keys``: one flag for two unrelated trust
  decisions.
- Gating only in the step input: a job author could weaken the connection
  without the operator's consent.

Consequences
------------

- Secure by default; weakening needs an operator setting per agent.
- No certificate pinning; the trust anchor is the CA set.
- The WinRM behavior is unproven on Windows until a VM job exists.
