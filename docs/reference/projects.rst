.. _reference-projects:

Projects
========

Projects form a tree. A project is identified by its ``path``, for example
``infra/billing``. Design: :doc:`/architecture/decisions/0062-project-tree`.

Project routes are authorized by role (:doc:`access-rules`): list and get need
``viewer`` (a list returns only projects you hold a role on), create needs
``admin`` on the parent (instance admin for a root project), update and delete
need ``admin``. No role at all is ``404``; too low a role is ``403``. The admin
token and instance-admin sessions are ``admin`` everywhere. A ``Project`` has
``my_role``, the caller's effective role. The examples below use the admin
token.

Paths
-----

A path has 1 to 8 segments separated by ``/``. Each segment matches
``^[a-z0-9][a-z0-9-]{0,62}$``. ``_inventory`` cannot be a project. ``default``
is a root project. Paths are immutable: there is no move or rename.

In a URL, ``{path}`` is one percent-encoded segment: ``infra%2Fbilling``.
Jobs select their project with ``spec.metadata.project``, which may be a nested
path.

Create and read
---------------

.. code-block:: shell

   curl -sX POST http://localhost:8080/api/v1/projects \
       -H "Authorization: Bearer $DAMSON_TOKEN" \
       -H 'Content-Type: application/json' \
       -d '{"path": "infra/billing", "description": "Billing infra"}'
   curl -s http://localhost:8080/api/v1/projects \
       -H "Authorization: Bearer $DAMSON_TOKEN"
   curl -s http://localhost:8080/api/v1/projects/infra%2Fbilling \
       -H "Authorization: Bearer $DAMSON_TOKEN"

The parent (``infra``) must exist; parents are not created for you. A bad path
or a missing parent gives ``422`` on ``body.path``; a duplicate gives ``409``.

Update and delete
-----------------

.. code-block:: shell

   curl -sX PATCH http://localhost:8080/api/v1/projects/infra%2Fbilling \
       -H "Authorization: Bearer $DAMSON_TOKEN" \
       -H 'Content-Type: application/json' \
       -d '{"if_version": 1, "description": "Billing"}'
   curl -sX DELETE http://localhost:8080/api/v1/projects/infra%2Fbilling \
       -H "Authorization: Bearer $DAMSON_TOKEN"

Only ``description`` can change. ``if_version`` fails with ``409`` if someone
else changed the project first.

``DELETE`` gives ``409`` and names the reason when the project still has child
projects, jobs, secrets in its scope path, or non-revoked service tokens scoped
exactly to it.

Audit
-----

``project.create``, ``project.update`` and ``project.delete`` (target type
``project``) record the path as ``project_path`` and details ``path`` and
``description``. Access rule writes are described in :doc:`access-rules`.
