ADR-0016: Pluggable log store with the database as default
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Step logs are the highest-volume data in Damson (§8.6). They must be durable,
tailed live from any controller, retained per project, and must not force object
storage on installations that only run Postgres.

Decision
--------

- Agents batch masked log lines (200 ms / 64 KiB) and send ``log.append``. The
  owning controller keeps a 5,000-line ring buffer for live tail and flushes
  chunks to a **LogStore** every 5 s or 1 MiB, inserting a ``log_chunks``
  manifest row, then acks the agent.
- ``LogStore`` (``Put``, ``Get``, ``Delete(prefix)``) has three backends:

  - ``db`` (default, including HA production): chunks in a dedicated
    ``log_chunks_data`` table; batched retention deletes.
  - ``fs``: dev or single controller with a shared volume.
  - ``s3`` (optional): any S3-compatible endpoint.

- Switching backends is a config change; old chunks stay readable through their
  stored backend id.
- The UI reads ``GET /api/v1/executions/{id}/logs?after_seq=N`` over SSE: stored
  chunks first, then the live tail (proxied to the owning controller); gaps are
  reported explicitly.

Alternatives Considered
-----------------------

S3 required
~~~~~~~~~~~

Disadvantages:

- Adds required infrastructure.

Log lines as rows
~~~~~~~~~~~~~~~~~

Disadvantages:

- Row-per-line write amplification and index bloat.

External log system (Loki, Elasticsearch)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Extra service; ack semantics outside Damson's control.

Decision Criteria
-----------------

Required infrastructure, durability, live tail, retention cost.

Rationale
---------

Chunking bounds write amplification; defaulting to the database keeps the
"Postgres only" promise; S3 is there when volume demands it.

Consequences
------------

Positive
~~~~~~~~

- Works out of the box; scales out with S3.

Negative
~~~~~~~~

- Database bloat for log-heavy installs on the ``db`` store.

Risks
~~~~~

- Unacked logs are lost when an agent crashes (accepted; gaps are shown).

Follow-up
~~~~~~~~~

- M2: ``db`` store and live logs. M9: ``s3`` store.

Validation
----------

Chaos test: logs are contiguous or show explicit gaps.

References
----------

- :doc:`/architecture/design` §10
