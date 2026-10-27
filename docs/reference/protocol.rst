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

Secrets
-------

The ``secrets`` field of ``step.execute`` is a ``SecretBundle``. Printing it
with ``fmt``, logging it with ``slog`` or marshaling it with
``encoding/json`` yields ``[REDACTED]``; only the MessagePack wire encoding
carries the values.
