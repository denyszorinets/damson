.. _reference-protocol:

Agent protocol
==============

Agents and controllers talk JSON-RPC 2.0 over a WebSocket. The package
``internal/protocol`` implements the wire format, the message types and a
bidirectional peer. ``ARCHITECTURE.md`` section 7 is the authoritative
description of the methods; this page covers the implementation.

Framing
-------

* Each WebSocket binary frame holds exactly one MessagePack-encoded JSON-RPC
  object: a request, a notification or a response. Text frames are a protocol
  violation and drop the connection.
* The maximum frame size is 4 MiB (``protocol.MaxFrameSize``). It is enforced
  when reading (the WebSocket read limit and ``DecodeMessage``) and when
  writing (``EncodeMessage`` and ``WSConn.WriteFrame``). An oversized incoming
  frame closes the peer; an oversized outgoing message fails the call with
  ``ErrFrameTooLarge`` and the connection stays up.
* ``params`` and ``result`` stay encoded until a handler calls
  ``Params.Decode``. Unknown fields are ignored.
* A frame that is not valid MessagePack or not a valid JSON-RPC object is
  answered with a ``-32700`` error (null id) and the connection stays open.

Error codes
-----------

.. list-table::
   :header-rows: 1

   * - Code
     - Meaning
   * - ``-32700``
     - Malformed frame
   * - ``-32600``
     - Invalid request
   * - ``-32601``
     - Method not found
   * - ``-32602``
     - Invalid params (returned by ``Params.Decode``)
   * - ``-32603``
     - Handler failed with an error that is not an ``*RPCError``
   * - ``-32000``
     - Server overloaded (too many requests in flight)
   * - ``-32001``
     - ``agent.hello`` refused: a newer ``session_seq`` owns the agent
   * - ``-32002``
     - ``agent.hello`` refused: the agent id belongs to another pool
   * - ``-32003``
     - ``agent.hello`` refused: unsupported protocol version

Peer
----

``protocol.NewPeer(conn, logger)`` wraps a ``Conn`` (``ReadFrame``,
``WriteFrame``, ``Close``). ``Run(ctx)`` owns the read loop and returns when the
connection fails, ``Close`` is called or ``ctx`` is cancelled. When it returns,
every request handler has finished.

* ``Call(ctx, method, params, &result)`` sends a request and waits for the
  correlated response. A JSON-RPC error response is returned as ``*RPCError``.
  Cancelling ``ctx`` abandons the call; a late response is dropped.
* ``Notify(ctx, method, params)`` sends a notification.
* ``Handle(method, handler)`` registers a handler for incoming requests and
  notifications. Notifications run in arrival order on the read loop and must
  not block; requests run concurrently.
* ``Close`` fails all pending and future calls with ``ErrClosed``.

``protocol.NewWSConn`` adapts a ``github.com/coder/websocket`` connection.

Versions
--------

``agent.hello`` carries ``protocol``. ``protocol.Negotiate`` accepts the current
version N (``ProtocolVersion``, 1) and N-1, and returns
``ErrUnsupportedVersion`` otherwise. With N = 1 only version 1 exists.

Sessions
--------

``agent.hello`` carries ``session_seq``, a per-agent-process connection counter
that starts at 1 and increases on every connection attempt. A new process has a
new instance id, so the counter is never persisted. The controller orders
competing hellos by ``session_seq`` (not wall clocks): only a strictly higher
value takes ownership of the agent row.

Secrets
-------

The ``secrets`` field of ``step.execute`` is a ``SecretBundle``. Printing it
with ``fmt``, logging it with ``slog`` or marshaling it with
``encoding/json`` yields ``[REDACTED]``; only the MessagePack wire encoding
carries the values.

Agent behavior
--------------

How the agent (``internal/agent``) answers the run messages. None of this
changes a message type.

``step.execute``
   Accepted when the plugin ``name`` at exactly ``plugin_version`` is built in
   and a slot is free. Otherwise the result is ``accepted: false`` with one of
   these reasons: ``unknown plugin <name>@<version>``, ``no free slot``,
   ``draining``, ``cancelled before start``, ``agent is stopping``. A repeated
   ``step.execute`` for an attempt the agent already holds is accepted again
   and not run twice. ``timeout_ms`` starts when the request is accepted.

``step.cancel``
   ``known: true`` for a running attempt, which gets its context cancelled; the
   plugin has ``grace_ms`` (5 s when zero) to return before the agent reports
   the step without it. For an attempt the agent does not hold, ``known`` is
   false and the attempt id is remembered (15 minutes, at most 4096 ids), so a
   ``step.execute`` that overtakes or follows the cancel is rejected with
   ``cancelled before start``.

``log.append`` and ``log.ack``
   Entry sequence numbers start at 1 per attempt and increase by one per entry.
   Batches are sent in order, flushed every 200 ms or 64 KiB (the hello
   ``limits`` may lower the size). Unacknowledged batches are resent, in order,
   after a reconnect, so the controller must accept duplicates. ``log.ack``
   for ``seq`` frees every batch ending at or before it.

   ``gap_to`` is optional and omitted when unset (additive; compatible with
   protocol N-1). When present, the sequence numbers ``seq_from`` to
   ``gap_to`` inclusive were dropped by the agent (spool overflow) and will
   never be sent; ``entries`` then start at ``gap_to + 1``. The controller must
   treat a declared gap as contiguous when it computes the ``log.ack`` value.
   The agent sends the gap together with a ``system`` entry that describes the
   loss and carries the last dropped sequence number.

``step.complete``
   Sent after the final log flush, and resent every two seconds, and on each
   new connection, until the answer has ``acknowledged`` or ``stale`` set.
   Results that are not yet acknowledged stay in memory and are listed in
   ``agent.hello`` ``executions`` with state ``finished``; running attempts
   with state ``running``.

``agent.hello`` reconciliation
   ``cancel`` for a running attempt cancels it (it then completes as
   ``canceled``). ``cancel`` for a finished attempt discards the result and its
   logs. ``continue`` and attempts missing from the list change nothing.

``agent.heartbeat``
   ``running_attempts`` lists the running attempts, ``free_slots`` is
   ``max_concurrent`` minus running (and abandoned) steps, zero while
   draining, and ``spool_bytes`` is the log spool size.

``node.hostkey_observed``
   Sent best-effort when a plugin reports a host key through
   ``ExecRequest.HostKeys``; ``key_type`` and ``fingerprint`` (``SHA256:...``)
   are derived from the key. A report made while disconnected is dropped.
