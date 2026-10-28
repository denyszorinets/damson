.. _reference-agent:

Agent connection
================

``internal/agent`` keeps one connection from an agent to its controller
(``ARCHITECTURE.md`` sections 7.1 to 7.3). ``damson agent`` and ``damson dev``
run it; configuration keys are on :doc:`configuration`.

Connecting
----------

The agent dials ``<agent.controller_url>/agent/v1/connect`` with
``Authorization: Bearer <pool token>`` and ``X-Damson-Agent-Id``. The id is the
hostname plus a random boot nonce, new on every process start. Plain ``ws://``
works only against a controller started with ``--insecure-dev`` (``dev``
always is). The token appears only in that header: it is never logged, and
errors are built from the HTTP status.

Right after the upgrade the agent sends ``agent.hello`` with:

* the protocol version and ``session_seq`` (1 on the first attempt of the
  process, then one more per connection attempt);
* the pool, the labels (``os`` and ``arch`` by default) and ``max_concurrent``
  (``agent.slots``, default CPU count times four);
* capabilities: ``bin:<name>`` for each of ``git``, ``ssh``, ``ansible``,
  ``ansible-playbook``, ``buildah`` and ``helm`` found on ``PATH``;
* the manifests of every built-in plugin (``plugins/all``).

Heartbeats
----------

The agent sends ``agent.heartbeat`` once straight after hello and then every
``heartbeat_interval_ms`` from the hello response (10 s by default), with free
slots, running attempts and host telemetry:

.. list-table::
   :header-rows: 1

   * - Field
     - Meaning
   * - ``cpu_percent``
     - Busy share of all cores since the previous heartbeat (0 on the first).
   * - ``memory_bytes``
     - ``MemTotal`` minus ``MemAvailable``.
   * - ``load1``
     - One-minute load average.

On Linux the values come from ``/proc/stat``, ``/proc/meminfo`` and
``/proc/loadavg``. Elsewhere, or when a file cannot be read, the affected
fields are zero; the agent still connects.

Reconnecting
------------

When the socket drops, the agent waits and dials again. The wait is drawn
uniformly from ``[0, min(30 s, 1 s * 2^n)]`` (exponential backoff with full
jitter, ``n`` counting failures since the last accepted hello). A hello that
was accepted restarts the sequence.

.. list-table::
   :header-rows: 1

   * - Event
     - Agent behavior
   * - HTTP 400, 401 or 403 on upgrade
     - Fatal: the agent stops and ``damson agent`` exits with status 1.
   * - Other HTTP status or network error
     - Retry with backoff.
   * - Hello error ``-32001`` (stale session)
     - Retry with backoff; the next attempt carries a higher ``session_seq``.
   * - Hello error ``-32002`` (pool conflict), ``-32003`` (unsupported
       protocol) or ``-32602`` (invalid params)
     - Fatal, as for authentication failures.
   * - ``agent.reconnect``
     - Close the socket, wait a random delay in ``[0, jitter_ms]``, reconnect
       (the backoff restarts).
   * - ``agent.drain``
     - Stop taking new work: heartbeats report zero free slots until the next
       session.

Embedding
---------

``agent.New(Config)`` validates the settings; ``Start(ctx)`` and ``Stop(ctx)``
own the goroutines. ``Done()`` closes when the loop ends and ``Err()`` holds
the fatal error. The clock, random source, host sampler and ``PATH`` lookup are
injectable, so the backoff and the loop are tested without sleeping.
