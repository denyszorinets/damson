.. _reference-webhooks:

Webhooks
========

A webhook starts a run of one job when an external system calls
``POST /hooks/<token>``. A job can have several named webhooks, and each one can
be revoked on its own. Design: :doc:`/architecture/decisions/0059-webhooks`.

Webhook management routes need ``Authorization: Bearer <admin token>`` until
M8. The ``/hooks/<token>`` call needs no other credential: the token in the URL
is the credential.

Create a webhook
----------------

.. code-block:: shell

   curl -sX POST http://localhost:8080/api/v1/webhooks \
       -H "Authorization: Bearer $DAMSON_TOKEN" \
       -H 'Content-Type: application/json' \
       -d '{
             "job_id": "<job id>",
             "name": "ci",
             "options": {"env": "staging"}
           }'

``job_id`` and ``name`` are required. ``name`` is unique among the job's
webhooks that are not revoked. ``options`` are static options for every call.
Without ``options_mapping`` they are checked against the job's ``spec.options``
at create time (defaults applied); invalid options give ``422`` with field
locations, never values.

The answer is the webhook plus ``token`` and ``path`` (``/hooks/<token>``). The
token is 43 characters and **is shown only in this answer**. Damson stores only
its SHA-256 hash, so it cannot be shown again.

Call a webhook
--------------

.. code-block:: shell

   curl -sX POST http://localhost:8080/hooks/<token> \
       -H 'Content-Type: application/json' \
       -d '{"anything": "your mapping reads"}'

The answer is ``202`` with ``{"run_id": "..."}``. The run has the trigger
``{"type":"webhook","webhook_id":"..."}``. The body is limited to 1 MiB.

Options for the run are the static ``options`` merged with the result of the
mapping (the mapping wins on a clash), then validated against the job's
``spec.options`` with defaults applied. Options marked ``x-damson-secret`` must
be ``secret://`` references. If validation fails the answer is ``422`` and no
run starts; messages name fields, never values.

An unknown token, a revoked token and a malformed token all answer ``404``;
unknown and revoked tokens look identical.

Mapping options from the request
--------------------------------

``options_mapping`` is a CEL expression that must produce a map. It sees only
three variables:

``body``
   The parsed JSON when the Content-Type contains ``json``; otherwise the body
   as text; ``null`` when the body is empty.

``headers``
   Header names in lower case, each with its first value. ``Authorization``,
   ``Proxy-Authorization`` and ``Cookie`` are removed.

``query``
   Each query key with its first value.

The job-spec variables are not available. The expression is checked when you
create the webhook; a bad expression gives ``422``.

Example for a GitHub-style push payload:

.. code-block:: shell

   curl -sX POST http://localhost:8080/api/v1/webhooks \
       -H "Authorization: Bearer $DAMSON_TOKEN" \
       -H 'Content-Type: application/json' \
       -d '{
             "job_id": "<job id>",
             "name": "github-push",
             "options": {"env": "staging"},
             "options_mapping": "{\"ref\": body.ref, \"commit\": body.after, \"pusher\": body.pusher.name}"
           }'

With a mapping, the static options are checked only when a request arrives, not
at create time. If the expression cannot be applied to a request (for example
the payload has no ``pusher``), the answer is ``422`` ``could not be applied to
this request``; it never quotes the request.

Rate limit
----------

Each token allows 5 requests per second with a burst of 20. Over the limit the
answer is ``429`` with a ``Retry-After`` header. The limit is kept in memory on
each controller, so with several controllers the total allowed is higher.

List, show and revoke
---------------------

.. code-block:: shell

   curl -s "http://localhost:8080/api/v1/webhooks?job_id=<job id>&limit=50" \
       -H "Authorization: Bearer $DAMSON_TOKEN"
   curl -s http://localhost:8080/api/v1/webhooks/<id> \
       -H "Authorization: Bearer $DAMSON_TOKEN"
   curl -sX DELETE http://localhost:8080/api/v1/webhooks/<id> \
       -H "Authorization: Bearer $DAMSON_TOKEN"

The list is cursor-paged (``next_cursor``). Revoking answers ``204`` and can be
repeated. The row stays with ``revoked_at`` set, so you can still read it; its
token stops working at once, and its name can be used again. ``last_used_at``
shows the latest call (updated at most once per minute).

There is no ``PATCH``. To change a webhook, revoke it and create a new one. The
new one has a new token, so this is also how you rotate a token. Deleting a job
deletes its webhooks.

Security notes
--------------

- Treat the URL like a password. Anyone who has it can start the job with the
  webhook's static options. Put it in the sender's secret store, not in a
  repository.
- Request logs show the path as ``/hooks/[redacted]``. Proxies and load
  balancers in front of Damson may still log the full URL; redact it there too.
- Use ``secret://`` references for secret options. Put no secret values in
  ``options`` or in the payload.
- Revoke a webhook as soon as its URL may have leaked.

Audit
-----

Creating and revoking a webhook write the audit events ``webhook.create`` and
``webhook.revoke`` (target type ``webhook``; details ``job_id``, ``name``,
``has_mapping``; never the token, its hash or the options). A call that starts a
run writes ``run.trigger`` (actor ``webhook:<webhook id>``; details include
``webhook_id``; never the options). See :ref:`reference-audit-api`.
