Jobs and runs
=============

Projects
--------

Projects form a tree, for example ``infra`` → ``infra/billing`` →
``infra/billing/reports``. A project carries access rules, a **node scope**
(a CEL selector that narrows the inventory), a retention policy and secrets.
Children inherit from their ancestors.

Jobs and versions
-----------------

A **job** is a named JSON spec in a project. Every change creates an immutable
**job version**; every run pins exactly one version, so reruns are exact
(:doc:`/architecture/decisions/0013-json-job-specs-as-versioned-data`).

A minimal spec:

.. code-block:: json

   {
     "apiVersion": "damson.dev/v1",
     "kind": "Job",
     "metadata": { "project": "infra/billing", "name": "uptime" },
     "spec": {
       "steps": [
         { "id": "check", "plugin": "ssh",
           "with": { "node": "ops-bastion", "command": "uptime" } }
       ]
     }
   }

``spec.options`` is a JSON Schema for run inputs; it also generates the run
dialog in the UI. ``spec.targets.selector`` picks nodes from the inventory once,
at run start.

Runs, step instances and executions
-----------------------------------

.. list-table::
   :widths: 25 75

   * - **Run**
     - One invocation of a job version, with resolved options and a status.
   * - **Step instance**
     - One logical step in a run, identified by a path such as
       ``main/for_each[web-01]/install``. Fan-out creates one per node.
   * - **Execution**
     - One attempt of a step instance on one agent. Retries create new
       executions with new ``attempt_id`` values.

Runs are started from the UI, the API, a cron schedule or a webhook.

Control flow
------------

Steps in a list run **sequentially**. The rules, evaluated by the pure engine
(:doc:`/architecture/decisions/0009-pure-deterministic-engine`):

- ``when`` is a CEL condition; the default is ``success()``. False means
  ``skipped``. ``failure()``, ``always()`` and ``cancelled()`` are also
  available.
- A failed step skips the rest of its list unless it sets
  ``continue_on_error`` or a later step's ``when`` allows it.
- A **group** can have ``catch`` (runs on failure, with ``error`` bound),
  ``finally`` (always runs) and ``rethrow``.
- ``for_each`` runs a group once per item in independent **lanes**, limited by
  ``max_parallel``; ``fail_threshold`` cancels pending lanes after N failures.
- ``retry`` reschedules failed attempts with exponential backoff when the error
  matches ``retry_on``.
- ``timeout`` applies per attempt; the job-level ``timeout`` covers the run.

.. mermaid::

   flowchart TB
     start((start)) --> fetch["fetch (transfer, ↻ 4)"]
     fetch --> grp
     subgraph grp["group install"]
       unpack["unpack (ssh)"]
       unpack -. on failure .-> rollback["catch: rollback"]
       unpack --> cleanup["finally: cleanup"]
       rollback --> cleanup
     end
     grp --> report["report (when: always())"]
     report --> done(((end)))

The full example behind this diagram is in :doc:`/architecture/design` §5.1.

Expressions and data passing
----------------------------

Any string in ``with`` can contain ``${{ <CEL> }}``. A string that is exactly
one expression keeps its type; otherwise results are concatenated as text.
Steps read earlier results through ``steps.<id>.outputs``. Inputs are resolved
on the controller when a step becomes runnable and stored, without secret
values, for audit.
