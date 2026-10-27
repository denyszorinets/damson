ADR-0006: Agent-initiated WebSocket with JSON-RPC 2.0 over MessagePack
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Agents run in segmented networks, behind NAT and firewalls, and as Kubernetes
pods. The controller must push work and cancellations to agents; agents must
stream logs, heartbeats and completions back. Both directions need requests and
notifications. The connection must survive controller restarts by reconnecting
to any controller behind a load balancer.

Decision
--------

- Agents **connect out** to ``wss://<controller>/agent/v1/connect``. Nothing
  connects inbound to agents. Plain ``ws://`` only with ``--insecure-dev``.
- Library: ``github.com/coder/websocket``.
- Authentication on the upgrade request: ``Authorization: Bearer <pool token>``
  plus ``X-Damson-Agent-Id``. Pool tokens are stored as SHA-256 hashes and are
  revocable. mTLS can be layered on by configuration.
- Framing: one binary WebSocket frame carries one **JSON-RPC 2.0** object
  encoded with **MessagePack** (``vmihailenco/msgpack/v5``); maximum 4 MiB.
  Both sides send requests and notifications. The layer is in-house (~300 LOC)
  in ``internal/protocol``.
- ``agent.hello`` carries ``protocol: 1``; the controller supports N and N-1.
  Changes to message types update ARCHITECTURE §7.2 in the same change
  (hard rule 8).

Alternatives Considered
-----------------------

gRPC bidirectional streaming
~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Typed IDL, mature tooling.

Disadvantages:

- Protobuf code generation and HTTP/2 requirements through proxies and
  ingresses; heavier than needed for a dozen methods.

Controller-initiated connections (SSH-style push)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Requires inbound reachability of every agent; fails through NAT and in k8s.

NATS as transport
~~~~~~~~~~~~~~~~~

Rejected in :doc:`0005-no-message-broker`.

JSON text frames
~~~~~~~~~~~~~~~~

Disadvantages:

- Larger log payloads; binary data needs base64.

Decision Criteria
-----------------

Network reachability, proxy and ingress compatibility, bidirectionality,
payload efficiency, code size.

Rationale
---------

WebSockets pass through standard HTTPS load balancers and ingresses. JSON-RPC
gives a known envelope for bidirectional calls; MessagePack keeps log batches
compact while staying schema-light.

Consequences
------------

Positive
~~~~~~~~

- Works in any network where the agent can reach the controller over HTTPS.
- One small in-house codec, fully under test.

Negative
~~~~~~~~

- Damson maintains its own RPC layer and versioning discipline.

Risks
~~~~~

- Breaking protocol changes. Mitigation: N-1 support, versioned hello, hard
  rule 8.

Follow-up
~~~~~~~~~

- M1: codec, pool tokens, hello/heartbeat, reconnect with backoff.

Validation
----------

Codec round-trip tests; property tests for fencing and leases; M1 end-to-end
reconnect test.

References
----------

- :doc:`/architecture/design` §7
- :doc:`0008-fencing-tokens-and-execution-leases`
