ADR-0025: Kerberos authentication for WinRM
================================================================================

:Status: Proposed
:Date: 2026-10-27

Context
-------

The ``winrm`` plugin supports Basic and NTLM over HTTPS in v1. Many Windows
estates disable NTLM and require Kerberos. Kerberos needs keytabs or
credentials, KDC reachability from agents, and a Go Kerberos implementation.

Decision
--------

Not decided yet. This ADR records the question and the options so they can be
evaluated; it changes nothing until it is accepted. Post-v1.

Alternatives Considered
-----------------------

Defer (v1: Basic/NTLM only)
~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Smaller v1 scope.

Disadvantages:

- Excludes NTLM-hardened estates.

Pure-Go Kerberos (``jcmturner/gokrb5``, Apache-2.0)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- No cgo; static binary preserved.

Disadvantages:

- Complex configuration (krb5.conf, keytabs as secrets).

System GSSAPI via cgo
~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Breaks the static, cgo-free binary (ADR-0002).

Decision Criteria
-----------------

- Customer demand.
- Static binary and air-gap constraints.
- Secret handling for keytabs.

Rationale
---------

Pending the decision.

Consequences
------------

Positive
~~~~~~~~

- To be recorded with the decision.

Negative
~~~~~~~~

- To be recorded with the decision.

Risks
~~~~~

- Deciding late may force changes to already-shipped behaviour; see Context.

Follow-up
~~~~~~~~~

- If accepted, add a ``keytab`` secret type.

Validation
----------

Nightly test against a Windows domain controller and member server.

References
----------

- :doc:`/architecture/design` §6.2, §22 item 4
- :doc:`0002-single-go-binary-with-controller-and-agent-roles`
