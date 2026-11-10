ADR-0059: Webhooks
==================

:Status: Accepted
:Date: 2026-11-07

Issue :issue:`195`. Implements the webhook trigger of ARCHITECTURE section 12.3.

Context
-------

External systems (Git hosts, CI) must be able to start a run of one job with an
HTTP call, without a user session. The URL is the only credential, so the token
must be unguessable, never stored in clear, and never logged.

Decision
--------

**Table.** Migration 00015 adds ``webhooks(id, job_id, name, token_hash,
options, options_mapping, revoked_at, last_used_at, version, created_at,
updated_at)``. ``job_id`` is a foreign key with cascade delete; ``token_hash``
is unique; ``options`` is JSON text; ``options_mapping`` is text. A partial
unique index on ``(job_id, name) WHERE revoked_at IS NULL`` makes a name unique
among active webhooks, so it can be reused after a revoke.

**Token.** 32 random bytes in base64url: 43 characters, no prefix. It is
returned once in the create response (``token`` and ``path``, which is
``/hooks/<token>``). Only the SHA-256 hex is stored and looked up.

**Revoke.** ``DELETE /api/v1/webhooks/{id}`` answers 204 and is idempotent. The
row stays with ``revoked_at`` set, so listings and audit stay readable. There
is no ``PATCH`` yet: to change a webhook, revoke it and create a new one, which
also rotates the token.

**Endpoint.** ``POST /hooks/{token}`` has no other authentication; the token is
the credential.

- A malformed token gives 404.
- Rate limit per token: a token bucket of 5 requests per second with a burst of
  20, keyed by the hash of the token, per controller, in memory. It is not
  durable state. Over the limit gives 429 with ``Retry-After``.
- Unknown and revoked tokens get an identical 404.
- The body is limited to 1 MiB.
- The static ``options`` are merged with the result of the optional
  ``options_mapping``; the mapping wins. The result is validated against the
  job's ``spec.options`` with defaults applied, and ``x-damson-secret`` options
  must be ``secret://`` refs. Failure gives 422; messages name fields, never
  values.
- The run is created with the trigger ``{"type":"webhook","webhook_id":..}`` in
  one transaction with the ``last_used_at`` update (written at most once per
  minute; it fails if the webhook was revoked meanwhile, which stops the run)
  and the ``run.advance`` enqueue. The answer is ``202
  {"run_id":...}``.

**Secret options.** A mapping may not set an option whose schema is or
contains ``x-damson-secret`` (422, ``options.<key>``): such options come from
the static options set by the administrator, so a caller cannot pick which
secret a run uses. At create time secret options must be ``secret://`` refs
even when a mapping is set, because the options are stored. Sensitive headers
(Authorization, Proxy-Authorization, Cookie, X-Gitlab-Token, X-Api-Key) are not
visible to the mapping.

**Mapping.** CEL in a dedicated environment with only the variables ``body``,
``headers`` and ``query``; the job-spec variables are not available.

- ``body`` is the parsed JSON when Content-Type contains ``json``, else the
  text, and null when empty.
- ``headers`` maps lower-cased names to the first value, with
  ``Authorization``, ``Proxy-Authorization`` and ``Cookie`` removed.
- ``query`` maps each key to its first value.
- The expression must produce a map. It is compile-checked at create time.
- An evaluation failure answers 422 ``could not be applied to this request``
  and never quotes the request.
- With a mapping, the static options are not schema-checked at create (only per
  request); without one they are.

**Logs.** The request log path is ``/hooks/[redacted]``.

**Audit.** ``webhook.create`` and ``webhook.revoke``, target type ``webhook``,
actor ``admin-token:<12 hex>``. Details are ``job_id``, ``name`` and
``has_mapping``; never the token, its hash or the options.

**API.** Admin token until M8 (amended by ADR-0076: project roles). ``GET`` and ``POST /webhooks``; ``GET`` and
``DELETE /webhooks/{id}``; list filter ``job_id``; cursor paging.

Deviation from ARCHITECTURE
---------------------------

ARCHITECTURE section 12.3 described ``options_mapping`` as "a CEL map from
``{body, headers, query}`` to options". It is implemented as a CEL expression
over the variables ``body``, ``headers`` and ``query``. Section 12.3 now says
so.

Consequences
------------

- The URL is a bearer secret: anyone who has it can start the job with the
  stored static options. Keep it secret; rotate by revoke and create.
- The rate limit is per controller, so the effective limit with several
  controllers is higher than the nominal one.
- Without ``PATCH``, changing a webhook means a new token.

References
----------

- :doc:`0053-audit-events`
- :doc:`0058-schedules-and-the-scheduler-duty`
- ARCHITECTURE sections 12.3, 12.4 and 14
