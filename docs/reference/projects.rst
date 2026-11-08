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

Only ``description`` can change here (the node scope has its own route, below). ``if_version`` fails with ``409`` if someone
else changed the project first.

``DELETE`` gives ``409`` and names the reason when the project still has child
projects, jobs, secrets in its scope path, or non-revoked service tokens scoped
exactly to it.

.. _reference-project-node-scope:

Node scope
----------

A project limits which inventory nodes its jobs can use with ``node_scope``, a
CEL expression over one variable, ``node``: the plain node map that selectors
use (``id``, ``name``, ``address``, ``tags``, ``attributes``, ``pool``, ``os``,
``transport``; never a credential, host key or port). An empty scope allows
every node. The effective scope is the AND of the project's scope and those of
all its ancestors, so a child can only narrow. Design:
:doc:`/architecture/decisions/0066-project-node-scope`.

.. code-block:: text

   'prod' in node.tags
   node.attributes.env == 'prod' && node.os == 'linux'
   node.name.startsWith('web-')
   node.pool == 'build'

.. code-block:: console

   curl -sX PUT http://localhost:8080/api/v1/projects/infra%2Fbilling/node-scope \
     -H "Authorization: Bearer $DAMSON_TOKEN" -H 'Content-Type: application/json' \
     -d '{"node_scope": "node.pool == '"'"'build'"'"'", "if_version": 3}'
   curl -s http://localhost:8080/api/v1/projects/infra%2Fbilling/node-scope \
     -H "Authorization: Bearer $DAMSON_TOKEN"

Reading and writing need the ``admin`` role on the project. The body is
``{node_scope, if_version}`` and the response ``{node_scope, version}``. The
version is the project version, shared with the description and the access
rules; a stale ``if_version`` is ``409``. The expression is at most 2048 bytes
and is checked on write (``422`` at ``body.node_scope``).

Where it applies:

* Dispatch: a node outside the scope fails the step permanently as
  ``unknown node``, the same as a missing node, so existence is not revealed.
* Run targets: ``targets`` holds only enabled, in-scope nodes, frozen at
  trigger. A run that already started keeps its targets, but dispatch checks
  the scope again for every step.

A scope that does not compile, or fails to evaluate for a node (for example
``node.attributes.region`` when the node has no ``region``), allows no node.
Controllers cache the project chain for 10 seconds, so a change applies
everywhere within about 10 seconds, and a tighter scope can take that long to
reach a controller.

The nodes API (list, get, create) stays instance-admin only and shows every
node: node scope limits what jobs can use, not what the inventory shows.

Audit
-----

``project.create``, ``project.update`` and ``project.delete`` (target type
``project``) record the path as ``project_path`` and details ``path`` and
``description``. Access rule writes are described in :doc:`access-rules`.
``project.node_scope.update`` records ``path`` and ``set`` (whether a scope is
set), never the expression.
