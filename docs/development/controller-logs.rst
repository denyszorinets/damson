Controller log ingest
=====================

``internal/controller/logs`` is the controller half of the agent log path. The
agent half is on :doc:`agent`; the wire messages are on
:doc:`/reference/protocol`. The rules come from ARCHITECTURE sections 7.2, 7.3
and 10.

What it does
------------

For every running execution that sends ``log.append`` to this controller, the
``Ingest`` component:

1. Fences the batch: it looks up the execution that owns ``attempt_id``. An
   unknown or superseded attempt is discarded, produces no ring or chunk
   writes, increments ``Stats.StaleBatches`` and is answered with a ``log.ack``
   so the agent frees its buffer.
2. Drops sequence numbers that were already ingested (the agent resends
   unacknowledged batches after a reconnect) and keeps a per-execution
   high-water mark. A declared ``gap_to`` range counts as contiguous. A batch
   that skips sequence numbers without declaring a gap is treated the same
   way and counted in ``Stats.LinesHoled``.
3. Appends the lines to a 5,000 line ring buffer (live tail) and to the pending
   chunk.
4. Writes the pending lines as chunks every 5 s or 1 MiB, whichever comes
   first, and on ``Flush`` or ``Finish``.
5. Sends ``log.ack`` with the highest contiguous sequence number that is
   committed, or declared a gap, and only after the chunk row committed.

Lines stay in the pending buffer until their chunk row is committed, so the ack
position can never run ahead of the database. Acks are coalesced: at most one
``log.ack`` per attempt per pass of the loop (default 1 s), however many chunks
or duplicate batches caused it. A duplicate batch is re-acknowledged, because
the agent only resends when it missed the ack.

Chunk format
------------

A chunk is one LogStore object plus one ``log_chunks`` row.

* Object key: ``logs/<execution_id>/<seq_from>-<seq_to>.ndjson``.
* Body: newline-delimited JSON, one object per line:
  ``{"t": <unix ns>, "s": "out|err|sys", "l": "<text>"}``. The sequence number
  is not stored: line *i* has ``seq_from + i``, so a chunk only ever holds a
  contiguous run. A gap in the middle of a flush becomes two chunks.
* Backend: ``db`` unless ``Config.Backend`` says otherwise.

``logs.ReadChunk(data, seqFrom)`` decodes a chunk and restores the sequence
numbers; ``logs.EncodeChunk`` is its inverse.

Idempotency and fencing at the store
------------------------------------

* The object is written with ``PutLogData`` first, then the manifest row with
  ``InsertLogChunk``, fenced by the attempt id.
* ``ErrConflict`` means a chunk already starts at that sequence number (an
  earlier flush, possibly by another controller, whose ack was lost). The
  persisted part is skipped and the rest of the run is written as a new chunk.
* ``ErrStale`` or ``ErrNotFound`` means the attempt is no longer current: the
  buffered lines are discarded and the execution's live tail is closed.
* Any other error keeps the lines and is retried on the next pass. No ack is
  sent for a run that did not commit.
* The attempt to execution lookup is cached for 5 s (unknown attempts for 1 s)
  in a bounded LRU. It is only a hint to save queries: the chunk insert is the
  authoritative fence. When another attempt currently owns the buffer of an
  execution, the cache is bypassed and the store decides.

Gaps are not stored inside chunks: a declared ``gap_to`` range (or a hole) is
simply a range of sequence numbers covered by no ``log_chunks`` row. The stored
log reader in ``internal/api`` derives its ``gap`` events from that, so the
encoding needs nothing more.

Live tail
---------

``Ingest`` satisfies ``api.LogTail`` (``Line`` is ``api.LogLine`` and
``ErrNoLiveTail`` is ``api.ErrNoLiveTail``). Pass the same value as
``api.Config.LogTail`` when wiring the controller.

``Subscribe(ctx, executionID, afterSeq)`` replays the buffered lines with
``seq > afterSeq`` and then delivers new ones. It returns ``ErrNoLiveTail``
when this controller holds no buffer for the execution. Ingest never blocks on
a subscriber: a subscriber whose queue (replay size plus
``Config.SubscriberQueue``) fills up is dropped and its channel closed, and
the client reconnects with ``after_seq``. The channel also closes on context
cancellation, ``Finish`` and ``Stop``.

Wiring (used by the dispatch and completion work)
-------------------------------------------------

.. code-block:: go

   in, err := logs.New(logs.Config{
       Store: st,
       Acks: func(ctx context.Context, agentID string, ack protocol.LogAck) error {
           peer, ok := gw.Peer(agentID)
           if !ok {
               return errors.New("agent not connected")
           }
           return peer.Notify(ctx, protocol.MethodLogAck, ack)
       },
       Logger: logger,
   })
   _ = in.Start(ctx)        // flush loop
   in.Register(peer, id)    // per agent session: installs the log.append handler
   _ = in.Finish(ctx, execID) // completion handler, after step.complete committed
   _ = in.Stop(ctx)         // flushes every pending chunk within ctx

``Register`` is called once per agent socket, next to the ``agent.hello`` and
``agent.heartbeat`` handlers. ``Finish`` flushes, releases the ring buffer and
closes subscribers; on error the state is kept so the caller can retry.
``Invalidate(attemptID)`` drops the cached lookup of an attempt; dispatch calls
it when it moves an execution to a new attempt.

Logs for an execution that is already terminal are still accepted as long as
the attempt id matches: a resend after the final ``step.complete`` must be
persisted and acknowledged so the agent can free its buffer. Executions that
receive nothing for 15 minutes without pending lines are evicted from memory.

Secrets
-------

Lines are masked by the agent before they are sent. The controller never logs
line text, only ids, counts and errors; a test checks this.

Metrics
-------

``Ingest.Stats()`` returns monotonic counters (lines ingested, duplicates,
stale batches, chunks written, conflicts, dropped subscribers, acks sent, flush
errors). They are the hook for the Prometheus metrics of ARCHITECTURE section
17.

Known limits
------------

* A transient store error while resolving the attempt makes ``log.append``
  fail without ingesting. The batch is not resent until the agent
  reconnects; the later lines then arrive with a skipped range that is
  treated as a gap.
* A store outage lets pending lines grow in memory until it recovers.
* A new controller that takes over an execution starts with an empty
  high-water mark and trusts the agent to resend only what is unacknowledged.
* Compaction into a single gzipped object on completion is not implemented.
