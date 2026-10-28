Agent internals
===============

``internal/agent`` runs steps for the controller. This page describes how the
pieces fit; the wire behavior is on :doc:`/reference/protocol` and the
operator view on :doc:`/reference/agent`. The rules come from ARCHITECTURE
sections 6, 7.3, 9.3, 9.4 and 10.

Components
----------

.. list-table::
   :header-rows: 1

   * - File
     - Responsibility
   * - ``session.go``
     - One connection: dial, ``agent.hello``, heartbeats, handler wiring.
       Registers ``step.execute``, ``step.cancel`` and ``log.ack`` on the peer.
   * - ``executor.go``
     - ``Executor`` (``Start``/``Stop``): slots, accept or reject, cancel,
       tombstones, heartbeat accessors. Owns every goroutine through a wait
       group; ``Stop`` cancels running steps and waits.
   * - ``run.go``
     - One step: builds ``pluginsdk.ExecRequest``, runs ``Validate`` then
       ``Execute`` on a goroutine, recovers panics, applies the grace period.
   * - ``complete.go``
     - Failure mapping, the finished-results map, the ``step.complete``
       delivery loop, hello reconciliation, attach and detach of a connection.
   * - ``masker.go``, ``outputs.go``, ``logger.go``
     - Secret masking of log lines, outputs, errors and the plugin logger.
   * - ``secrets.go``
     - ``bundleResolver``: the secret bundle held only in memory, wiped when
       the step ends.
   * - ``logpipe.go``
     - Per-attempt batcher: sequence numbers, 200 ms or 64 KiB flush.
   * - ``logbuf.go``, ``spool.go``
     - Unacknowledged log buffer (memory, then spool file) and the only sender
       of ``log.append``.
   * - ``hostkeys.go``
     - ``HostKeyReporter`` mapped to ``node.hostkey_observed``.
   * - ``link.go``
     - The current peer, swapped on every reconnect, so the executor outlives
       connections.

Life of a step
--------------

1. ``step.execute`` arrives. ``accept`` checks, under one lock and in this
   order: duplicate attempt (accepted again, not run twice), executor
   stopping, draining, tombstone, plugin ``name@version`` known, free slot.
   Any failure is a rejection with a reason. Otherwise the step gets a context
   derived from the executor's (with the timeout, if any) and a goroutine.
2. The goroutine builds the masker and the resolver from the bundle, starts
   the log pipe, and runs the plugin on an inner goroutine that holds the
   slot until the plugin returns.
3. When the plugin returns, or the context ends and the grace period passes,
   ``buildComplete`` maps the outcome to a status, sanitizes outputs and
   errors, and the pipe is closed (final flush). The step moves from
   ``runs`` to ``done``.
4. The delivery loop sends ``step.complete`` for every entry in ``done``
   whenever a connection is attached, a step finishes, or the retry
   interval passes. An acknowledged or stale answer removes the entry and its
   buffered logs.

Status mapping
--------------

.. list-table::
   :header-rows: 1

   * - Outcome
     - ``status``
     - ``error.code``
   * - ``Result.Error`` or error return
     - ``failed``
     - SDK error kind (``exit_code``, ``connection``, ``auth``, ...); a plain
       error is ``internal``
   * - ``Validate`` failed
     - ``failed``
     - ``validation``; ``Execute`` does not run
   * - Plugin panic
     - ``failed``
     - ``internal``, message ``plugin panicked`` (the value is never sent or
       logged: it can carry secrets)
   * - Failure while the step timeout had passed
     - ``timed_out``
     - ``timeout``
   * - Failure after ``step.cancel`` or a reconciliation cancel
     - ``canceled``
     - ``cancelled``
   * - Plugin still running ``grace`` after cancel or timeout
     - ``canceled`` or ``timed_out``
     - The plugin goroutine is abandoned but keeps its slot until it returns.
   * - Success
     - ``succeeded``
     - none

``retryable`` is set only for ``connection`` errors.

Secrets
-------

The bundle is decoded from ``step.execute`` and handed to ``bundleResolver``;
the request struct drops its reference. The masker is built from every field
value with its base64 (standard, URL, padded and raw) and URL-encoded forms,
and each line of five or more characters of a multi-line value. Masked paths:

* every log line, before it enters the batcher;
* ``step.progress`` messages;
* error messages in ``step.complete``;
* string values in outputs (the whole value becomes ``***``, a warning goes to
  the agent log and to the step log);
* the plugin ``Logger`` (message, string and error attributes).

``secrets_test.go`` proves that no frame the agent sends and no agent log line
contains any form of a secret, including on failure and panic.

Logs
----

``Emitter.Log`` masks, splits lines longer than 32 KiB, and appends to the
pipe. Sequence numbers start at 1 and increase by one per entry for the
attempt. The pipe flushes to the hub every 200 ms, or when the pending text
reaches the batch size (64 KiB, or the ``log_batch_bytes`` limit from the
hello response); a flush cuts batches at that size. A plugin that logs faster
than batches can be handed over blocks briefly (backpressure) instead of
growing memory.

The hub keeps every batch until ``log.ack`` covers it: in memory up to 32 MiB,
then in the spool, a ring in one file (256 MiB by default, ``agent.spool_dir``
and ``agent.spool_max_bytes``). When the spool is full the oldest spooled
batches are dropped and a ``system`` log line ``log gap: entries A-B were
dropped by the agent (log buffer full)`` goes into the attempt's stream. The
spool is deleted when the agent stops; an agent crash loses unacknowledged
logs. After a reconnect the hub resends everything unacknowledged, in order.
Only the hub sends ``log.append`` (one send lock), which keeps batches ordered
across reconnects.

Testing
-------

``fakectl_test.go`` is a scripted controller (WebSocket server plus
``protocol.Peer``) that records decoded messages and raw frames and lets a test
decide the ``agent.hello`` and ``step.complete`` answers. Failure and
reconnect scenarios are driven by closing its peer. Run
``go test -race -count=3 ./internal/agent/...``; the package uses ``goleak``.
