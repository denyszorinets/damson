ADR-0040: Internal controller endpoints
================================================================================

:Status: Accepted
:Date: 2026-11-04

Context
-------

The ring buffer of an execution's live log lives on the controller that owns
the execution (ARCHITECTURE 10). A UI request can reach any controller, so
controller X must be able to read the live tail from controller Y (ARCHITECTURE
8.5, issue #89). Without it, a stream opened on X sees only stored chunks, which
lag the live output by the flush interval. Controllers need an endpoint for
each other that agents and browsers cannot use, and a way to authenticate it
without adding infrastructure (ADR-0004).

Decision
--------

Listener
~~~~~~~~

A separate listener, ``controller.internal_listen`` (default ``:8081``), serves
``/internal/`` routes. It is started only when a cluster secret is set and
never in the ``dev`` role. The public mux answers ``404`` for ``/internal/``.
The transport is plain HTTP with an HMAC; TLS can follow later. The network
between controllers must therefore be private: log lines cross it
unencrypted.

Endpoint
~~~~~~~~

``GET /internal/v1/executions/{id}/tail?attempt_id=&after_seq=`` answers
``application/x-ndjson``, one object per line: ``{"type":"line","seq","ts",
"stream","line"}`` or ``{"type":"heartbeat"}`` (every 10 s when idle). It serves
only the owner's local ring buffer and never forwards, so requests cannot
loop. Answers: ``401`` (bare, no reason), ``404`` (no buffer), ``409`` (the
buffer holds another attempt; sequence numbers restart per attempt), ``503``
(more than 256 concurrent tails), ``400`` (bad parameters).

Authentication
~~~~~~~~~~~~~~

``internal/controller/internalauth``. A request carries ``X-Damson-Timestamp``,
``X-Damson-Caller`` and ``X-Damson-Signature``. The signature is HMAC-SHA256
over a version prefix, the method, the escaped path, the canonical query, the
timestamp, the caller id, the target controller id and the body hash. The key
is derived from the secret with the context string ``damson internal v1``. The
target id binds a request to one controller. The timestamp window is 30 s in
both directions. The endpoint is read-only, so no nonce cache is kept; the
service is stateless. There is no CIDR allowlist.

Secret
~~~~~~

``controller.cluster_secret`` (env ``DAMSON_CONTROLLER__CLUSTER_SECRET``) or
``controller.cluster_secret_file``, at least 32 bytes. ``controller.
cluster_secret_previous`` is accepted for verification only, so the secret can
be rotated without downtime. Secrets are redacted in ``Config.Redacted`` and
never logged. Config validation fails for a Postgres controller without a
secret or without a valid ``controller.internal_url``; SQLite and dev need
neither. ``internal_url`` is checked structurally: http or https, a host, no
userinfo, query, fragment or path.

Proxy
~~~~~

``internal/controller/tailproxy.Tail`` implements ``api.LogTail``. It tries the
local ring first. Otherwise it reads the execution's ``controller_id`` and that
controller's ``controller_instances.internal_url`` and calls the endpoint with a
signed request. The owner counts as gone (no live tail) when its ``last_seen``
is older than 15 s, the execution is terminal, unowned or ours, or the
registry row is missing. The client follows no redirects, connects with a 3 s
timeout, waits 5 s for headers and drops a tail silent for 30 s. Any failure
returns ``ErrNoLiveTail``; the SSE log stream then polls the stored chunks and
drops lines it already sent by sequence number. A stream without a live tail
tries again every 5 s (``api.StreamConfig.ResubscribeInterval``), which picks
up an execution adopted by another controller.

Wiring fix
~~~~~~~~~~

``node.Start`` never set ``api.Config.LogTail``. It now does: the local ring
without a secret, the proxy with one.

Alternatives Considered
-----------------------

Same listener with a path prefix
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Exposes the internal routes wherever the public API is exposed and relies on a
reverse proxy to hide them. Rejected: a separate port is simpler to firewall.

mTLS or a TLS-only listener
~~~~~~~~~~~~~~~~~~~~~~~~~~~

Needs certificate distribution. Deferred, not rejected; the HMAC scheme does
not preclude adding TLS.

Reading chunks only
~~~~~~~~~~~~~~~~~~~

No new surface, but the live tail of a remote execution would lag by the chunk
flush interval (5 s). It remains the fallback.

Consequences
------------

Positive
~~~~~~~~

- Live tail works from any controller and degrades to stored chunks when the
  owner is unreachable.
- Tests: an HMAC table including rotation, the internal handler, the proxy
  (redirects, stale owner, silent owner), a test that the secret is never
  logged, config validation, and chaos B5
  ``TestLogTailThroughOtherControllerSurvivesOwnerCrash``.

Negative
~~~~~~~~

- Log lines cross the controller network unencrypted, and the secret protects
  only integrity and caller identity. A private network is required.
- A new port to open between controllers, and a secret to distribute.
- Within the 30 s window a captured request can be replayed to read the same
  tail; the endpoint has no side effects.
