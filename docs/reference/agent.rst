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

The startup log line shows the configured ``agent.slots`` (``0`` means auto),
while the agent's own log lines show the resolved count.

Running steps
-------------

The controller sends ``step.execute``; the agent runs the named plugin (built
in, exact name and version) in its own goroutine, at most ``agent.slots`` at a
time. The plugin gets its input, the nodes of the step, a per-execution work
directory (removed afterwards) and a logger. Secret values arrive only in
``step.execute``, live only in the agent's memory, and are dropped when the
step ends; every log line, progress message, error message and string output
is masked before it leaves (plain, base64 and URL-encoded forms).

* ``step.cancel`` cancels the plugin's context. The plugin has the requested
  grace period to return; after that the agent reports the step ``canceled``
  anyway, and the plugin keeps its slot until it does return.
* A step with a timeout is reported ``timed_out`` when it expires.
* A plugin panic is reported as an ``internal`` failure; nothing of the panic
  value is sent.
* Logs are batched (200 ms or 64 KiB) and kept until the controller
  acknowledges them: first in memory (32 MiB), then in a spool file
  (``agent.spool_max_bytes``, 256 MiB by default, in ``agent.spool_dir``).
  When the spool is full the oldest unacknowledged logs are dropped and a
  ``system`` line records the gap. An agent crash loses unacknowledged logs.
* ``step.complete`` is retried until the controller acknowledges it, across
  reconnects. A result that is not acknowledged is listed in the next
  ``agent.hello`` so the controller can decide.
* ``agent.allow_insecure_host_keys`` lets the ``ssh`` plugin connect to nodes
  whose host key policy is ``insecure``. Off by default.

Internals are described in :doc:`/development/agent`.

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
jitter, ``n`` counting failures since the last stable session). A session
that stayed up for one heartbeat interval after hello restarts the sequence;
a connection that is accepted and dropped at once keeps the backoff growing.

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
