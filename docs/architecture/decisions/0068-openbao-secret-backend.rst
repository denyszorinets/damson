ADR-0068: OpenBao secret backend
================================

:Status: Accepted
:Date: 2026-11-08
:Amends: :doc:`0042-db-secret-backend-keyring` (scope note: the KEK rules apply to the ``db`` backend only), :doc:`0053-audit-events` (audit atomicity for secret writes)

Issue :issue:`217`. Refines ARCHITECTURE section 9.2. Related:
:doc:`0047-agent-side-masking-only`, :doc:`0065-audit-actors-and-coverage`,
:doc:`0064-access-rules-and-authorization`.

Context
-------

Installations that already run OpenBao (or Vault) want their secrets managed
there, with their own policies, audit device and rotation, and not a second
copy in the Damson database. ARCHITECTURE section 9.2 named an ``openbao``
backend but left its design open, and listed a ``SecretStore`` interface that
the code never had.

Decision
--------

**Client.** A small ``net/http`` client for KV v2 in ``internal/secrets/openbao``
(about nine endpoints), not ``github.com/openbao/openbao/api``. The SDK is
MPL-2.0, which the license policy allows, but it pulls ``go-retryablehttp``,
``go-cleanhttp``, ``go-rootcerts``, ``hcl``, ``mapstructure`` and ``go-jose``,
reads ``BAO_*`` and ``VAULT_*`` environment variables and follows redirects.
Our own client has none of that.

**Node interface.** The controller node uses a small ``secretBackend``
interface (``Put``, ``Get``, ``Meta``, ``List``, ``Delete``) satisfied by the
``db`` and ``openbao`` backends. Authorization (``secrets.Authorize`` and the
``_inventory`` rule) is unchanged and runs in the dispatcher before any
``Get``.

**KEK and canary are db-only.** ``secrets.backend = openbao`` with a KEK set
is a configuration error. A canary left in the database by an earlier ``db``
setup only logs a WARN. ``damson admin rotate-kek`` refuses. ``damson dev``
always uses ``db``. ``secrets.openbao.read_only`` makes writes return
``secrets.ErrReadOnly`` (HTTP ``409``, problem type ``conflict``); ``--dev-nodes`` with secrets and
``read_only`` fails at start.

**Ref mapping.** ``<mount>/data|metadata/<prefix>/<scope>/<name>``. Defaults:
mount ``secret``, prefix ``damson``. Mount, prefix and ``auth.mount`` have 1 to 8
segments matching ``[A-Za-z0-9][A-Za-z0-9._-]*`` and are validated in the
configuration. The prefix must not be empty (Damson would otherwise write into
the root of a shared mount). The mount must be KV v2.

**Metadata.** The type and field names live in KV ``custom_metadata``:
``damson_type`` and one ``damson_field_<name>`` = ``"1"`` per field. Foreign
keys are preserved. A missing ``damson_type`` lists as ``generic``. The data is
written first, then the metadata, so a crash in between leaves a generic secret
that the next ``Put`` re-types. A secret written outside Damson without
``damson_type`` is adopted the same way: the next ``Put`` of any type gives it
that type. A metadata-only entry (for example from ``bao kv metadata put``) is
not a secret: ``Meta`` and ``Get`` answer not found, and ``Put`` with
``ifVersion`` 0 creates it.

**Writes.** ``Put`` uses KV ``cas``: ``ifVersion`` 0 is create-only, ``n`` is
``cas`` n, and nil reads the current version and retries up to 3 times. Writes
are never retried automatically. A soft-deleted or destroyed current version is
not found. ``Delete`` removes all versions (metadata delete). If the data
version was written but the metadata write fails, the new version is still
audited (``secret.update`` with that version) and the error is returned.

**Reads.** ``GET`` and ``LIST`` retry twice on network errors, ``429`` and
``5xx``. ``Get`` requires string values that satisfy the field and size grammar
of ADR-0042; anything else is ``ErrUnavailable`` (permanent) and is logged with
the ref only. Response bodies are capped at 2 MiB; an oversized or malformed answer is
``ErrUnavailable`` (permanent), never an endless retry. ``List`` walks folders
(cap 10,000 keys, an error beyond), sorts by (scope, name) bytewise and reads
metadata for the requested page only (8 concurrent requests). It keeps reading
past dropped entries (a soft-deleted current version, metadata-only entries with
``current_version`` 0) until the page is full or the walk ends, so pages are
never short.

**Authentication.**

- ``token``: ``token_file`` (re-read on re-login) or ``DAMSON_OPENBAO_TOKEN``.
- ``approle``: ``role_id`` or ``role_id_file``; ``secret_id_file`` or
  ``DAMSON_OPENBAO_SECRET_ID``; auth mount default ``approle``.
- ``kubernetes``: ``role``; auth mount default ``kubernetes``;
  ``service_account_token_file`` default
  ``/var/run/secrets/kubernetes.io/serviceaccount/token``, re-read at every login.

The login token is renewed at 2/3 of its lease; when renewal fails the client
logs in again. A ``403`` does not log in at once: the client first calls
``auth/token/lookup-self`` with the token that failed. If that also answers
``403`` or ``401``, it logs in again (one login for any number of concurrent
callers, serialized by a login mutex) and retries once; a second ``403`` is
``ErrUnavailable``. If the token is still valid the ``403`` stands as a policy
denial and is ``secrets.ErrUnavailable`` (permanent). If the re-login fails with
an outage, the transient error is returned instead, because the secret does not
look unusable. Reason: denied refs must not burn AppRole ``secret_id`` uses or
tokens. Logins do not hold the state mutex across HTTP, so ``Stop`` never waits
for a slow server. A failed refresh with a transient error keeps the current
token and retries in about 30 s; a definite failure clears the token and the run
loop backs off. ``Stop`` revokes login tokens (best effort, 5 s).

**Startup.** A definite rejection (``400``, ``401``, ``403`` or ``404`` on login
or ``lookup-self``, an unreadable credential file, a TLS certificate
verification failure) refuses to start. An outage (network error, ``5xx``)
starts degraded with an ERROR log and keeps retrying the login with a 1 s to
30 s backoff. Meanwhile dispatch requeues and the API answers ``503``. There is
no KV probe at start.

**Transport.** ``https`` is required unless ``allow_plain_http``. The address
is scheme and host only. ``tls_ca_file`` adds roots; verification cannot be
disabled. Redirects are refused. ``namespace`` is sent as ``X-Vault-Namespace``.
``request_timeout`` defaults to 10 s (maximum 1 m). An address with userinfo is
rejected. With a plain ``http://`` address and ``HTTP_PROXY`` set, the token
goes to the proxy in clear text; plain http is opt-in, prefer https.

**Cache.** ``cache_ttl`` defaults to 0 (off), maximum 30 s. It applies to the
dispatch path only (``GetForDispatch``), keyed by scope and name, and hands out
copies. One global generation counter means any write through this controller
invalidates reads already in flight (they cannot repopulate stale data); entries
of other keys stay. The entry is evicted after every write or delete attempt,
also on errors and timeouts. At most 1024 entries (the oldest is evicted); a sweeper clears
expired maps. Eviction drops references only: Go strings cannot be zeroed.
Writes made through another controller or directly in OpenBao can be stale for
up to the TTL. A cached read does not reach OpenBao's audit log; Damson's
``secret.read`` event is still written.

**Audit.** ``secret.create``, ``secret.update`` and ``secret.delete`` are still
written (type, KV version, field names; no ``kek_id`` or ``secret_id``), but
**after** the change succeeded and not in one transaction. This departs from
ADR-0053 ("same transaction, fail closed"), because an OpenBao write cannot join
a database transaction. A missing actor refuses the write before any request.
If appending the event fails, the error says "the secret changed but the audit
event was not recorded" and an ERROR is logged (action, scope, name, actor),
because the change cannot be rolled back. Changes made directly in OpenBao are audited by
OpenBao's audit device. ``secret.read`` per dispatched attempt (ADR-0065) is
unchanged.

**Configuration.** ``secrets.backend`` (``db`` or ``openbao``);
``secrets.openbao.{address,namespace,mount,prefix,read_only,tls_ca_file,allow_plain_http,request_timeout,cache_ttl}``;
``secrets.openbao.auth.{method,token_file,role_id,role_id_file,secret_id_file,role,mount,service_account_token_file}``.
``DAMSON_OPENBAO_TOKEN`` and ``DAMSON_OPENBAO_SECRET_ID`` are environment-only
and never TOML keys. ``Redacted()`` masks the token, secret id and role id.
See :doc:`/reference/secrets-openbao`.

Alternatives Considered
-----------------------

- **The official OpenBao API module.** Rejected for the dependency weight and
  behaviour listed above.
- **Copy db secrets into OpenBao on switch.** Rejected: it would put plaintext
  through a migration path. Secrets are recreated in OpenBao.
- **Keep the audit event in a transaction by writing it first.** Rejected: it
  would record changes that failed.
- **A KV probe at start.** Rejected: the policy may allow only some paths.

Decision Criteria
-----------------

Few dependencies, no secret value outside the dispatch path, the same
authorization rules for both backends, and a clear behaviour during outages.

Rationale
---------

The interface is the set of methods the node already used, so authorization,
masking and delivery stay as they were. Writing the audit event after the
change keeps a failed change out of the log; the rare failure to append is
reported loudly to the caller instead of hidden.

Consequences
------------

Positive
~~~~~~~~

- Secrets can be managed under OpenBao policies and audit, optionally read-only.
- No new module dependency.

Negative
~~~~~~~~

- Secret writes are not atomic with their audit event.
- No migration from ``db``; no rotation tool for OpenBao-held data.
- A cache makes reads up to ``cache_ttl`` stale across controllers.

Risks
~~~~~

- OpenBao down: dispatch of steps with secrets waits and the secrets API answers
  ``503`` until it is back.
- Edits made directly in OpenBao appear as ``generic`` secrets without field
  names until re-saved through Damson.

Follow-up
~~~~~~~~~

- Per-project backends (the node interface already allows it).

Validation
----------

A shared contract suite (``internal/secrets/secretstest``) runs over the ``db``
backend on SQLite and over the ``openbao`` backend against a fake server
(``baotest``). A real OpenBao 2.6.5 dev container runs under the ``integration``
tag in CI. A test in the dispatcher proves masking with the openbao backend.
Unit tests cover the cache, authentication, redirects and TLS.

References
----------

- :doc:`0042-db-secret-backend-keyring`
- :doc:`0053-audit-events`
- :doc:`0065-audit-actors-and-coverage`
- :doc:`/reference/secrets-openbao`
