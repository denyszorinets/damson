.. _reference-job-spec:

Job spec v1
===========

A job is a JSON document (``apiVersion: damson.dev/v1``, ``kind: Job``); the
design is in ``ARCHITECTURE.md`` section 5. This page documents what the
implementation in ``internal/engine`` and ``internal/expr`` supports today
(milestone M2: sequential plugin steps) and what is reserved for M3.

The JSON Schema is generated from the Go types (``engine.Schema``) and
committed at ``testdata/engine/job-v1.schema.json``; ``make gen`` refreshes
it. ``engine.Parse`` validates a document against that schema and then applies
the rules below. Errors carry precise paths such as ``spec.steps[1].id`` and
all problems are reported at once.

Document layout
---------------

.. code-block:: json

   {
     "apiVersion": "damson.dev/v1",
     "kind": "Job",
     "metadata": { "project": "infra/billing", "name": "deploy", "description": "" },
     "spec": {
       "options": { "type": "object", "properties": { "env": { "type": "string" } } },
       "targets": { "selector": "'web' in node.tags" },
       "timeout": "30m",
       "steps": [
         { "id": "fetch", "plugin": "transfer@1", "timeout": "10m",
           "with": { "key": "app/${{ inputs.env }}.tgz" } },
         { "id": "report", "plugin": "ssh", "when": "always()",
           "continue_on_error": true, "with": { "command": "echo ${{ run.status }}" } }
       ]
     }
   }

.. list-table::
   :header-rows: 1
   :widths: 25 75

   * - Field
     - Rules
   * - ``spec.options``
     - A JSON Schema of ``type: object`` describing run inputs. It must itself
       be a valid schema.
   * - ``spec.targets.selector``
     - A CEL expression. Compiled for syntax now; evaluated by the inventory
       work later.
   * - ``spec.timeout``
     - Go duration (``30s``, ``10m``), positive. Covers the whole run.
   * - ``spec.steps``
     - At least one step; run strictly in order.
   * - ``steps[].id``
     - Required, ``[a-z0-9-]+``, unique within the list.
   * - ``steps[].plugin``
     - Required: ``name`` or ``name@<major>``. Whether the plugin exists, and
       its input schema, are checked by the caller against the plugin
       registry, on the *resolved* ``with``.
   * - ``steps[].with``
     - Plugin input. Any string may contain ``${{ }}`` expressions; they are
       compile-checked at validation time.
   * - ``steps[].timeout``
     - Per attempt, Go duration. Passed to the executor as data.
   * - ``steps[].when``
     - CEL condition, default ``success()``.
   * - ``steps[].continue_on_error``
     - A failure is recorded but does not fail the run or skip later steps.
   * - ``steps[].runs_on``
     - ``{ "pool": "...", "labels": { ... } }``; at least one of the two.

Reserved fields (rejected for now)
----------------------------------

These fields are part of the schema so that M3 extends the model instead of
reshaping it, but validation rejects them with ``<path> is not supported yet``:
``spec.concurrency``, ``type: "group"``, and on steps ``steps``, ``catch``,
``finally``, ``rethrow``, ``for_each``, ``max_parallel``, ``fail_threshold``
and ``retry``.

Control flow (sequential)
-------------------------

``engine.Engine.Decide(job, state, now)`` is a pure function (no I/O, no
clock, no randomness; ``now`` is injected) returning the actions to apply. It
implements ``ARCHITECTURE.md`` section 5.3 rules 1 to 3 and the status
functions of rule 8:

* Steps run in order. A step is created only when every earlier step is
  terminal.
* Before a step runs its ``when`` is evaluated; false means ``skipped``.
* After a failure without ``continue_on_error`` the rest of the list is
  skipped, except steps whose ``when`` calls ``failure()``, ``always()`` or
  ``cancelled()``.
* A ``when`` that calls no status function is evaluated as
  ``success() && (when)``. So ``inputs.env == 'prod'`` never runs after a
  failure, while ``failure() && steps.a.status == 'failed'`` does.
* ``success()`` is true while no earlier step failed (unhandled) and the run
  is not cancelled. ``failure()`` is true after an unhandled failure.
  ``cancelled()`` is true once cancel was requested. ``always()`` is true.
* A ``when`` or ``with`` expression that fails to evaluate fails that step
  with error kind ``expression``; the rest follows rule 3.
* On cancel, in-flight executions that started before the request are
  cancelled; pending steps become ``cancelled`` unless their ``when`` allows
  them to run (an ``always()`` cleanup still runs). The run ends ``cancelled``
  if any step was cancelled.
* The run ends ``failed`` if any step failed without ``continue_on_error``,
  otherwise ``succeeded``.

Actions are ``StartRun``, ``CreateStepInstance``, ``ScheduleExecution``,
``SkipStep``, ``FailStep``, ``CancelExecution`` and ``CompleteRun``. The
caller applies them in order inside one transaction guarded by
``runs.version``. An empty result means "wait". The step timeout is carried on
``ScheduleExecution``.

Expressions
-----------

Interpolation uses ``${{ <CEL> }}`` inside any string of ``with``.

* A string that is exactly one expression keeps the CEL type (number, bool,
  list, map): ``"retries": "${{ inputs.retries }}"`` resolves to a JSON number.
* Otherwise each result is stringified (strings as is, ``null`` as empty,
  everything else as compact JSON) and concatenated.
* ``$${{`` yields a literal ``${{``. The closing ``}}`` is the first one
  outside a quoted string.
* Map keys are not interpolated.

Context variables: ``inputs``, ``run`` (``id``, ``status``, ``trigger``,
``triggered_by``, ``started_at``), ``job`` (``name``, ``project``,
``version``) and ``steps.<id>`` (``status``, ``outputs``, ``error``,
``started_at``, ``finished_at``). ``node``, ``targets`` and ``error`` compile
but are bound only from M3. Whole JSON numbers are exposed to CEL as integers,
so ``inputs.count + 1`` works. The ``strings`` extension functions are
available.

Secrets are never values in the context. ``secret://`` references are plain
strings that pass through resolution unchanged and are resolved at dispatch
time.

Expressions run under limits (``expr.Limits``): a CEL cost budget (default
100,000), a maximum result size (64 KiB per expression) and a maximum source
length (4 KiB). Exceeding one fails the expression with a clear error; the
resolved input of a step is capped at 1 MiB.

Checksum
--------

``engine.Checksum`` is the SHA-256 of the canonical JSON form (sorted keys, no
insignificant whitespace, numbers verbatim, no HTML escaping), so formatting
and key order do not change a job version's checksum.
