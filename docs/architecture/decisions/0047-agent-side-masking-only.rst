ADR-0047: Only the agent masks secrets
======================================

:Status: Accepted
:Date: 2026-11-06

Context
-------

ARCHITECTURE 9.4 promised that the controller re-applies masking on ingest and
keeps hashes of the secret values for the run. The code never did this: there
is no masking code in ``internal/controller``. Implementing it would keep
secret-derived material in controller memory, which :doc:`0015-secret-references-and-envelope-encryption`
rules out (:issue:`120`).

Decision
--------

- Only the agent masks. The controller does not re-mask.
- After ``step.execute`` is sent the controller keeps no secret values, hashes
  or derived material; the bundle is wiped after send.
- What the controller stores and serves (log chunks, ring buffer and live tail,
  outputs, errors, progress, host key reports) is exactly what the agent sent.
- Defense in depth lives in the agent: masking at every exit (log lines,
  progress, plugin logger, errors, outputs, host key reports), fail-closed
  after the bundle is wiped, and only masked lines in the spool.
- ARCHITECTURE 9.4, 18 and 20 and the security page are corrected.

Known limits
~~~~~~~~~~~~

- Values shorter than 4 bytes.
- Lines of 4 characters or fewer inside multi-line values.
- Secrets split across ``Log`` calls.
- Values the job transforms (hex, reversed, re-encoded).
- A compromised agent, which holds the plaintext anyway.

Masking is defense in depth, not a guarantee.

Tests
~~~~~

- Agent wire-level tests over raw frames, including reconnect and spool resend.
- End-to-end tests on every controller sink, on both databases.
- An HA move reconnects the same agent process, so the reconnect and resend tests cover it; a dedicated chaos test is not added.

Alternatives Considered
-----------------------

- A keyed rolling hash held by the dispatching controller: fails exactly under
  HA adoption unless the hashes are persisted, which creates an offline
  brute-force oracle and violates "only refs are stored". It repeats the
  agent's limits, costs time on the socket read loop and keeps secret-derived
  material in controller memory for hours.
- Re-resolving the bundle on adoption: reads secrets outside the dispatch path,
  may mask a rotated version and adds audit reads.
- A secret-free entropy or PEM scanner on the controller.

Consequences
------------

- The controller holds no secret-derived material after dispatch.
- A leak past the agent's masker is persisted and served as sent.
- Follow-up (not part of this change): operators may need a way to purge one
  execution's logs after a leak.
