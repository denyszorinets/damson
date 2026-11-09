.. _reference-audit:

Audit
=====

Every mutation and trigger writes an audit event in the same transaction as the
change. Events are insert-only and never contain secret values. Design:
:doc:`/architecture/decisions/0053-audit-events` and
:doc:`/architecture/decisions/0065-audit-actors-and-coverage`.

Actors
------

.. list-table::
   :header-rows: 1

   * - Actor
     - Meaning
   * - ``user:<sub>``
     - An OIDC login.
   * - ``sa:<service account id>``
     - A service account token.
   * - ``admin-token:<12 hex>``
     - The admin token (first 12 hex of its hash).
   * - ``cli:<os user>``
     - A CLI command such as ``rotate-kek``. A label, not proof of identity.
   * - ``system:<duty>``
     - ``scheduler``, ``retention``, ``dispatch`` or ``dev-nodes``.
   * - ``webhook:<webhook id>``
     - A run started by a webhook.
   * - ``anonymous``
     - A job or run call without credentials while login and ``require_token``
       are off.

``actor_name`` is a display snapshot taken when the event is written, for
example a user's email at that time. It is never refreshed; ``actor`` is the
identity.

Actions
-------

.. list-table::
   :header-rows: 1

   * - Action
     - Target type
     - Details
   * - ``secret.create``, ``secret.update``, ``secret.delete``, ``secret.kek_rotate``
     - ``secret``, ``instance``
     - ids, versions, field names; never values
   * - ``secret.read``
     - ``execution`` (target: execution id)
     - ``attempt_id``, ``run_id``, ``refs`` (sorted ``secret://scope/name``)
   * - ``job.create``, ``job.version.create``
     - ``job``
     - ``name``, ``version``, ``checksum``; ``source`` when written by apply
   * - ``job.archive``, ``job.restore``, ``job.manage``, ``project.manage``
     - ``job``, ``project``
     - apply only; ``job.manage`` and ``project.manage`` have ``from`` and
       ``to`` (the owner); see :doc:`jobs-as-code`
   * - ``run.trigger``
     - ``run``
     - ``job_id``, ``job_version_id``, ``trigger``, ``schedule_id`` or
       ``webhook_id``; never options
   * - ``run.cancel``
     - ``run``
     - ``job_id``
   * - ``auth.login``
     - ``user`` (target: ``sub``)
     - ``instance_admin``
   * - ``project.create``, ``project.update``, ``project.delete``
     - ``project``
     - see :doc:`projects`
   * - ``project.access_rules.update``, ``job.access_rules.update``
     - ``project``, ``job``
     - see :doc:`access-rules`
   * - ``schedule.create``, ``schedule.update``, ``schedule.delete``
     - ``schedule``
     - see :doc:`schedules`
   * - ``webhook.create``, ``webhook.revoke``
     - ``webhook``
     - see :doc:`webhooks`
   * - ``service_account.create``, ``service_account.delete``,
       ``service_token.create``, ``service_token.revoke``
     - ``service_account``, ``service_token``
     - see :doc:`service-accounts`
   * - ``log.purge``
     - ``execution``
     - ``execution_id``, ``run_id``, ``chunks``, ``objects``
   * - ``admin_token.create``, ``admin_token.revoke``
     - ``instance``
     - none
   * - ``metrics_token.create``, ``metrics_token.revoke``
     - ``instance``
     - none (target ``metrics_token``)
   * - ``audit.prune``
     - ``instance``
     - retention batch (:doc:`configuration`)
   * - ``run.prune``
     - ``instance`` (target ``runs``)
     - counts ``runs``, ``executions``, ``log_chunks``, ``log_objects``,
       ``queued_log_deletes`` and ``before``; actor ``system:retention``; no
       run content (:doc:`configuration`)

``job.create`` and ``job.version.create`` are written when the caller has an
actor; imports and tests without one write none. ``run.trigger`` is written for
API, webhook and every scheduler fire (one event per fire, actor
``system:scheduler``).

Secret reads
~~~~~~~~~~~~

``secret.read`` is written once per dispatched attempt that carries secrets, by
the dispatcher (actor ``system:dispatch``) before ``step.execute`` is sent. If it
cannot be written, the execution is requeued and the secrets are not sent. A
send that fails after the event leaves an event for a read that did not
complete, so the log can over-report but never under-report. Each retry of an
execution writes its own event.

Who can read
------------

``GET /api/v1/audit`` filters: ``project``, ``target_type``, ``target``,
``cursor``, ``limit`` (1 to 200, default 50). Newest first.

- An instance admin (admin token or ``admin_rule`` user) sees all events.
- With ``project=<path>``, an admin of that project sees events whose
  ``project_path`` is the project or a descendant.
- No role on the project gives ``404``; a lower role gives ``403``.
- Without ``project``, a caller who is not an instance admin gets ``403``.
- Events with an empty ``project_path`` (instance level) are visible only to
  instance admins.

Example
-------

.. code-block:: console

   $ curl -s -H "Authorization: Bearer $TOKEN" \
       "http://localhost:8080/api/v1/audit?project=infra&target_type=execution&limit=20"
