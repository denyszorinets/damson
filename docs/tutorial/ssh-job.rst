Run a job over ssh
==================

This walks through the M2 end-to-end path on a development setup: ``damson
dev`` with a ``--dev-nodes`` file, one ``ssh`` job created through the API,
triggered, and its status and logs read back. ``--dev-nodes`` is development
only and stands in for inventory and the secret store until M4 (see
:ref:`cli-dev-nodes` and
:doc:`/architecture/decisions/0034-dev-only-inline-nodes-and-secrets`).

Prerequisites
-------------

- A reachable sshd and a user that can log in to it.
- ``damson`` built, and ``curl``.

Describe the node and its secrets
---------------------------------

Write ``nodes.json`` (file format: :ref:`cli-dev-nodes`). Secret values live in
separate files that are mode 0600 or stricter:

.. code-block:: json

   {
     "version": 1,
     "nodes": {
       "sshd": {
         "address": "127.0.0.1",
         "port": 2222,
         "transport": "ssh",
         "os": "linux",
         "credential": "secret://dev/sshd",
         "host_key": "ssh-ed25519 AAAA...",
         "host_key_policy": "strict"
       }
     },
     "secrets": {
       "secret://dev/sshd": {"username": "demo", "private_key_file": "/home/me/dev/id_ed25519"},
       "secret://dev/token": {"value_file": "/home/me/dev/token.txt"}
     }
   }

Start Damson
------------

.. code-block:: console

   $ damson dev --dev-nodes nodes.json

It logs a WARN naming the file and the node names. The examples below assume
the API on ``localhost:8080``; use the address in ``controller.listen``.

Create the job
--------------

.. code-block:: bash

   curl -s -X POST localhost:8080/api/v1/jobs \
       -H 'Content-Type: application/json' -d '{"spec":{
         "apiVersion":"damson.dev/v1","kind":"Job",
         "metadata":{"project":"default","name":"hello-ssh"},
         "spec":{"steps":[{"id":"hello","plugin":"ssh","with":{
           "node":"sshd",
           "command":"echo hello-from-sshd; echo t=$TOKEN",
           "env":{"TOKEN":"secret://dev/token"}}}]}}}'

The response carries the job ``id``.

Trigger a run and read it
-------------------------

.. code-block:: console

   $ curl -s -X POST localhost:8080/api/v1/jobs/<job-id>/runs
   $ curl -s localhost:8080/api/v1/runs/<run-id>
   $ curl -N localhost:8080/api/v1/executions/<execution-id>/logs

The run response lists its steps and their executions; the logs endpoint is
Server-Sent Events (:doc:`/reference/http-api`).

The log shows ``hello-from-sshd`` and then ``t=***``: the token is a secret, so
the agent masks it before the line leaves the agent.

Test
----

The same flow runs as ``cmd/damson/e2e_ssh_test.go`` (build tag
``integration``), against an sshd container, with ``dev`` and with a Postgres
controller plus a separate agent.
