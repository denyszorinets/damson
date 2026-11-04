ADR-0039: Leader lease
================================================================================

:Status: Accepted
:Date: 2026-11-03

Context
-------

Some duties must run on one controller at a time: the reaper, the cleanup of
dead ``controller_instances`` rows, and later the scheduler and retention
(ARCHITECTURE 8.4, issue #88). Until now the reaper ran on every controller.
That was safe because each expiry is a compare-and-swap, but it multiplied the
work and judged lease expiry with the fastest clock. There is no consensus
service (ADR-0004), so the database must arbitrate.

Decision
--------

Table
~~~~~

Migration 00009 (both dialects) adds ``leader_leases(name PK, holder, term,
acquired_at, expires_at)``, one row per singleton duty; today only ``reaper``.
``holder`` is the controller id and has no foreign key, because
``controller_instances`` rows are deleted on shutdown. ``term`` increases by 1
on every change of holder and never resets; release only sets
``expires_at = now``.

Acquire, renew, release
~~~~~~~~~~~~~~~~~~~~~~~

``store.LeaderStore.AcquireLeader`` is one upsert. It succeeds if the caller is
the holder or the current lease has expired (``expires_at < now``). A takeover
bumps ``term``; a renewal keeps it. Release is ``UPDATE ... SET expires_at =
now WHERE holder AND term match``.

Clock
~~~~~

``now`` is the injected Go clock in both backends (not the database clock).
Skew only shifts the failover time (TTL plus skew). It cannot cause flapping,
and a brief double leader is harmless because the duties are idempotent.

Fence
~~~~~

``Tx.CheckLeader(name, holder, term, now)`` returns ``ErrStale`` unless the
lease is held at that term and unexpired. The reaper calls it first in its reap
transaction. The check is strict on SQLite (exclusive transaction) and best
effort on Postgres READ COMMITTED, which is enough for idempotent duties.

Elector
~~~~~~~

``internal/controller/leader`` uses a 15 s TTL and renews every 5 s
(``node.Timing.LeaderTTL`` / ``LeaderRenew``). ``Term()`` reports leadership
only while ``now < expires_at - renew``, so a controller cut off from the
database steps down before another can take over. ``Stop`` releases the lease
so a graceful handover is faster than the TTL. It runs on SQLite as well, so
there is one code path.

Scope
~~~~~

``dispatch.Config.Leader`` gates the reaper and the dead-controller cleanup;
``nil`` means every controller runs them (tests). The leader deletes
``controller_instances`` rows with ``last_seen`` older than 1 minute, since
crashed controllers never deregister; agent rows are not touched. The sweep
(River unique key) and the per-controller duties (dispatch, lease renewal,
cancels, adoption) stay on every controller. The scheduler (M7) will need the
lease plus a unique ``(schedule_id, fire_at)``.

Alternatives Considered
-----------------------

Database clock
~~~~~~~~~~~~~~

Judging expiry with ``now()`` of the database removes skew between controllers
but needs dialect-specific SQL. Rejected: skew only delays failover.

Postgres advisory locks
~~~~~~~~~~~~~~~~~~~~~~~

Not available on SQLite, which would need a second code path. Rejected.

Consequences
------------

Positive
~~~~~~~~

- One reaper at a time, with a fence in the transaction and a single code path
  on both databases.
- The chaos tests ``TestLeaderFailoverAfterControllerCrash`` and
  ``TestLeaderHandoverOnGracefulStop`` cover crash failover and graceful
  handover.

Negative
~~~~~~~~

- After a crash the reaper pauses for up to the TTL plus clock skew.
- On Postgres the fence is best effort, so a short double leader is possible and
  relies on idempotent duties.
- The SQLite startup lock row claimed in ARCHITECTURE 8.1 did not exist when
  this ADR was written. *Update (issue #101):* it is now the lease row
  ``sqlite-controller``, taken at startup through ``node.Config.SingleController``
  (set for the SQLite backend by ``damson controller``/``dev``): a second
  controller fails to start while it is held and waits out a crashed one's lease
  (at most the ttl plus one renewal); losing it later only logs, since
  correctness rests on compare-and-swap.
