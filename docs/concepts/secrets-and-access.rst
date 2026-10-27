Secrets and access
==================

Secret references
-----------------

Specs never contain secret values. They contain references:

.. code-block:: text

   secret://<project-path>/<name>[#<key>]
   secret://infra/billing/deploy-key#private_key

A job may use secrets of its own project or any ancestor. Inventory
credentials live under ``secret://_inventory/``. Secret types include
``ssh_key``, ``password``, ``token``, ``s3``, ``registry``, ``kubeconfig`` and
``generic``.

Values are resolved only when a step is dispatched, sent to the agent with the
step, masked in all logs and outputs, and dropped when the step ends. By
default they are stored in the database with envelope encryption; OpenBao is
the optional alternative (:doc:`/architecture/security`).

Access control
--------------

Users sign in with OIDC. Access is granted by rules on projects and jobs, each
a role plus a CEL condition over the user's claims:

.. code-block:: json

   { "role": "runner", "rule": "'billing-ops' in claims.groups" }

.. list-table::
   :header-rows: 1
   :widths: 15 85

   * - Role
     - Can
   * - ``viewer``
     - See jobs, runs and logs.
   * - ``runner``
     - Also trigger and cancel runs.
   * - ``editor``
     - Also change jobs, schedules and webhooks.
   * - ``admin``
     - Also change access rules, secrets and child projects.

Rules on a project apply to all its descendants. The effective role is the
highest role any matching rule grants.
