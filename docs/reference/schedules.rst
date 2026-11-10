.. _reference-schedules:

Schedules
=========

A schedule starts runs of one job on a cron timetable. The ``scheduler`` duty
runs on the controller that holds the ``scheduler`` lease and checks every 10
seconds, so a run starts within about 10 seconds of its fire time. Each fire
time starts at most one run, even with several controllers. Design:
:doc:`/architecture/decisions/0058-schedules-and-the-scheduler-duty`.

A schedule belongs to a job and follows its project's roles (ADR-0076):
``viewer`` lists and reads, ``editor`` creates, changes (pause included) and
deletes; the job's own rules count, and a caller with no role on the job gets
``404``. The admin token and instance admins may do everything. ``GET
/api/v1/schedules`` needs ``job_id`` or ``project`` (the project and those below
it) unless the caller is an instance admin or holds a token covering every
project. Fires run as ``system:scheduler``, whoever created the schedule.

Create a schedule
-----------------

.. code-block:: shell

   curl -sX POST http://localhost:8080/api/v1/schedules \
       -H "Authorization: Bearer $DAMSON_TOKEN" \
       -H 'Content-Type: application/json' \
       -d '{
             "job_id": "<job id>",
             "name": "nightly",
             "cron": "30 2 * * *",
             "timezone": "Europe/Berlin",
             "options": {"env": "staging"},
             "misfire": "run_once"
           }'

``job_id``, ``name`` and ``cron`` are required. ``name`` is unique per job.
``timezone`` defaults to ``UTC``, ``enabled`` to ``true`` and ``misfire`` to
``run_once``. ``options`` are checked against the job's current
``spec.options`` (defaults applied); invalid options give ``422`` with field
locations, never values. The answer is the schedule, including ``next_fire_at``.

.. code-block:: shell

   curl -s "http://localhost:8080/api/v1/schedules?job_id=<job id>&limit=50" \
       -H "Authorization: Bearer $DAMSON_TOKEN"
   curl -s http://localhost:8080/api/v1/schedules/<id> \
       -H "Authorization: Bearer $DAMSON_TOKEN"

The list is cursor-paged (``next_cursor``). ``PATCH`` changes any of ``name``,
``cron``, ``timezone``, ``options`` (replaces the whole value), ``enabled`` and
``misfire``. Send ``if_version`` to fail with ``409`` if someone else changed
the schedule first.

.. code-block:: shell

   curl -sX PATCH http://localhost:8080/api/v1/schedules/<id> \
       -H "Authorization: Bearer $DAMSON_TOKEN" \
       -H 'Content-Type: application/json' \
       -d '{"if_version": 3, "enabled": false}'
   curl -sX DELETE http://localhost:8080/api/v1/schedules/<id> \
       -H "Authorization: Bearer $DAMSON_TOKEN"

``next_fire_at`` is recomputed when ``cron`` or ``timezone`` change or a
schedule is enabled again. A disabled schedule has no ``next_fire_at`` and never
fires. Deleting a job deletes its schedules.

Cron syntax
-----------

Five fields: minute, hour, day of month, month, day of week.

.. code-block:: text

   30 2 * * *       every day at 02:30
   */15 * * * *     every 15 minutes
   0 9 * * 1-5      09:00 on weekdays

The descriptors ``@hourly``, ``@daily``, ``@weekly``, ``@monthly``, ``@yearly``
and ``@midnight`` are accepted. Not accepted: a seconds field, ``@every``, and
``TZ=`` or ``CRON_TZ=`` inside the expression; use the ``timezone`` field.

Time zones and daylight saving time
-----------------------------------

``timezone`` is an IANA name such as ``Europe/Berlin`` or ``America/New_York``.
The zone database is built into the binary.

- An expression that names the hour (for example ``30 2 * * *``) fires once
  when the clocks go back (at the first occurrence), and does not fire that day
  when its time does not exist (when the clocks go forward).
- An expression with a wildcard hour (``*``, ``*/n``, ``@hourly``) runs
  through the change normally.

Missed fire times
-----------------

If the controllers were down and a fire time is more than 1 minute past due:

``run_once``
   Starts one run, not one per missed time.

``skip``
   Starts no run and sets ``last_error`` to
   ``skipped a missed fire time (misfire: skip)``.

In both cases ``next_fire_at`` moves to the next time after now.

Runs and ``last_error``
-----------------------

A run from a schedule has the trigger
``{"type":"schedule","schedule_id":"...","fire_at":"<RFC 3339>"}``. Options,
targets and the first step are handled as for a run started through the API.
``last_fire_at`` and ``last_run_id`` show the latest run started.

``last_error`` says why a fire time made no run, or warns about the schedule:

- A skipped missed fire time (above).
- The job's options no longer validate (the spec changed). No run is made, the
  message names the fields only, and the schedule moves on to its next time.
  Fix the options with ``PATCH``.
- The expression can no longer be evaluated. The schedule fires this time and
  looks again in 1 hour.

Database errors do not set ``last_error``: the schedule stays due and is retried
on the next tick.

Audit
-----

Creating, changing and deleting a schedule write the audit events
``schedule.create``, ``schedule.update`` and ``schedule.delete`` (target type
``schedule``; details ``job_id``, ``name``, ``cron``, ``timezone``, ``enabled``,
``misfire``, ``version``; never the options). Every fire writes one ``run.trigger``
event (actor ``system:scheduler``; details ``job_id``, ``job_version_id``,
``trigger``, ``schedule_id``; never the options) in the transaction that creates
the run. See :ref:`reference-audit-api`.
