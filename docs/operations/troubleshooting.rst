Troubleshooting
===============

Each row is a symptom, the likely cause and the fix. Messages are quoted from
the code. Logs are JSON; filter on ``level`` and the attribute shown. See
:doc:`monitoring` for the log fields.

Agents
------

.. list-table::
   :header-rows: 1
   :widths: 28 32 40

   * - Symptom or message
     - Cause
     - Fix
   * - Agent exits with status 1; controller answered 403 ``plain ws:// is not
       allowed; connect with wss://``
     - The connection did not arrive over TLS, and the controller is not in
       ``insecure_dev``.
     - Use a ``wss://`` URL. With native TLS on the controller nothing else is
       needed; a private CA needs ``agent.controller_ca_file`` on the agent.
       Behind a TLS proxy set
       ``controller.trust_forwarded_proto = true`` and make the proxy send
       ``X-Forwarded-Proto: https``. Only development uses ``insecure_dev``.
   * - 401 ``a pool token is required`` or ``the pool token is unknown or
       revoked``
     - Missing, mistyped or revoked token (the agent stops and exits 1).
     - Create a token with ``damson admin token create --pool NAME --name
       NAME``; check ``token list``. The token belongs to one pool.
   * - 400 ``X-Damson-Agent-Id header is missing or invalid``
     - A proxy removes the header, or a custom client.
     - Forward all headers on ``/agent/v1/connect``.
   * - Hello refused: ``pool does not match the pool token``
     - ``agent.pool`` differs from the pool of the token.
     - Fix ``agent.pool`` or use the right token.
   * - Hello refused: ``agent id is registered in another pool``
     - The agent id (hostname plus nonce) already belongs to another pool.
     - Check ``agent.pool`` and the pool of the token. The agent id is the
       hostname plus a boot nonce, so two agents sharing an id is unusual.
   * - Agent connects, drops, reconnects in a loop
     - A proxy or load balancer idle timeout shorter than the heartbeat (10 s),
       or no WebSocket support.
     - Allow WebSocket upgrades and an idle timeout of at least a minute.
       The agent backs off up to 30 seconds.
   * - WARN ``plugins not offered: the agent lacks the programs they need``
       (agent log, ``missing``)
     - The agent host has no ``ansible``, ``buildah``, ``helm``, ``git`` or
       ``ssh`` that a plugin needs.
     - Use the ``damson-agent`` or ``damson-agent-build`` image
       (:doc:`/reference/images`), or install the tool and restart the agent.

Controller start and readiness
------------------------------

.. list-table::
   :header-rows: 1
   :widths: 28 32 40

   * - Symptom or message
     - Cause
     - Fix
   * - ``/readyz`` 503
     - The database does not answer within 3 seconds, or it is not migrated.
       The controller log says which: ``database ping``, ``migration version``
       or ``no migrations applied``.
     - Fix the database connection, or run ``damson migrate up`` or start a
       controller with ``--auto-migrate``. ``damson migrate status`` shows what
       is pending. The WARN ``database is not migrated; agent gateway
       and run.advance disabled`` confirms the second case.
   * - ``damson admin`` says ``database is not migrated; run "damson
       controller --auto-migrate"``
     - ``admin`` commands need a migrated database.
     - Run ``damson migrate up``, or start a controller once with
       ``--auto-migrate``.
   * - ``damson migrate up`` says the database is newer than this binary
     - The database was migrated by a newer release; migrations do not go back.
     - Run the newer ``damson``. To return to the old version, restore the
       pre-upgrade backup (:doc:`upgrade`).
   * - Start fails ``checking the KEK: secret encryption key problem: the KEK
       does not match this instance (configured kek_id X, instance kek_id Y)``
     - The key is not the one that encrypted the database (or the previous key
       in ``DAMSON_KEK_PREVIOUS``).
     - Set the right key. If you rotated and lost the old key, restore the key
       from your escrow (:doc:`backup`).
   * - Start fails ``this instance has encrypted secrets but no KEK is
       configured``
     - The database holds secrets and the controller has no KEK.
     - Set ``DAMSON_KEK`` or ``secrets.kek_file``.
   * - Start fails: setting both ``DAMSON_KEK`` and ``secrets.kek_file``
     - Both are set.
     - Use one.
   * - WARN ``secrets are disabled: no KEK configured``; runs fail with
       ``step uses secret:// refs but no secret source is configured``
     - No KEK on an instance without secrets.
     - Set a KEK, restart all controllers, and create the secrets.
   * - Start fails with ``controller.cluster_secret ... is required with the
       postgres driver``
     - PostgreSQL controllers need a cluster secret of at least 32 bytes.
     - Set ``DAMSON_CONTROLLER__CLUSTER_SECRET`` and
       ``controller.internal_url`` (:doc:`/reference/configuration`).
   * - Start fails on ``controller.internal_url``
     - Not an ``http`` or ``https`` URL with only scheme, host and port.
     - Use for example ``http://10.0.0.5:8081``.
   * - Start fails ``another controller is using this SQLite database``
     - A second controller on SQLite.
     - Use PostgreSQL, or stop the other controller. After a crash, wait for
       the lease to expire (about 20 seconds).
   * - Start fails with S3 bucket probe error
     - ``logs.store = s3`` and the bucket is unreachable or credentials lack
       rights.
     - Check endpoint, credentials and the permissions in
       :doc:`/reference/log-store`.
   * - ERROR ``openbao is unreachable; secrets are unavailable until it
       answers``
     - OpenBao is down or unreachable.
     - Fix the network or OpenBao; the controller recovers on its own.
   * - Config error: ``... allow_plain_http`` / address refused
     - An ``http`` URL for OpenBao or S3 without the flag.
     - Use https (:doc:`hardening`).

Login
-----

.. list-table::
   :header-rows: 1
   :widths: 28 32 40

   * - Symptom or message
     - Cause
     - Fix
   * - Browser shows ``Login did not stick: the browser did not keep the
       session cookie``
     - The session cookie is ``Secure`` with the ``__Host-`` prefix, so the
       browser drops it over plain http or when the host differs.
     - Serve the UI over https on the host in ``redirect_url``. Only for
       development set ``auth.cookie_insecure``.
   * - 401 on ``/auth/callback``
     - The login state is invalid, expired or replayed, the binding cookie is
       missing (the callback hit a different site name than the login) or the
       id token is bad.
     - Start again. Check that ``auth.oidc.redirect_url`` matches the public
       URL and ends in ``/auth/callback``.
   * - 502 on login
     - The identity provider is unavailable.
     - Check the issuer URL and connectivity from the controllers.
   * - A user logs in but has no admin pages (403)
     - ``auth.oidc.admin_rule`` is false for the user; a role change lasts up
       to 12 hours.
     - Fix the rule or the group claim (``groups_claim``); log in again.
   * - The job and run pages fail in the browser with ``require_token`` and no
       OIDC
     - The UI has no login without OIDC.
     - Use OIDC, or leave ``require_token`` off and restrict by network.

Runs and dispatch
-----------------

.. list-table::
   :header-rows: 1
   :widths: 28 32 40

   * - Symptom or message
     - Cause
     - Fix
   * - Runs stay ``queued``
     - No connected agent in the step's pool, no free slot, or no agent in the
       pool offers the plugin at the pinned version (``unknown plugin
       name@version``, ``no free slot``). A plugin whose tools are missing is
       withheld by the agent (see Agents above).
     - Check the Agents page or ``GET /api/v1/agents`` for the pool and its
       capabilities. Add agents, raise ``agent.slots``, or fix the pool name in
       ``runs_on.pool``.
   * - Execution fails ``unknown secret`` or ``secret "..." has no field``
     - The ``secret://`` ref does not exist in that scope, or the field does
       not.
     - Create the secret, or fix the ref. The ref scope must be the run's
       project or ``_inventory``.
   * - Execution fails ``secret "..." : secret unavailable``; ERROR ``secret
       cannot be decrypted``
     - The stored secret does not decrypt under the current keys, or OpenBao
       cannot be read.
     - Check the KEK (:doc:`upgrade`) or OpenBao.
   * - Execution fails ``unknown node``, ``node is disabled`` or ``host key
       policy not available``
     - The target node does not exist, is disabled, or uses policy ``insecure``
       (always refused at dispatch).
     - Fix the node in the inventory; use ``strict`` or ``tofu``.
   * - ``step targets nodes in pools dc1, dc2; set runs_on.pool``
     - Targets live in several pools.
     - Set ``runs_on.pool`` or split the step.
   * - WARN ``execution lost: lease expired``
     - The agent stopped renewing: it died, was restarted, or its network
       failed for more than 60 seconds.
     - Check the agent. The job's retry policy decides what happens next.
   * - Steps cancelled on agent restarts
     - The steps outlived ``agent.drain_grace`` (default 20 s) when the agent
       stopped.
     - Raise ``agent.drain_grace`` and the stop timeout together, or restart
       agents when idle (:doc:`upgrade`).
   * - Log gaps; WARN ``log stream: chunk unreadable``
     - A chunk is listed but cannot be read: the S3 object is missing, the
       backend was removed from the configuration, or the bucket/prefix changed.
     - Keep the ``logs.s3`` block (with ``logs.store = db`` too). A restored
       bucket older than the database causes the same (:doc:`backup`).
   * - WARN ``log objects are queued for deletion but no external log store
       is configured on this controller``
     - A controller has no ``logs.s3`` block while deletes are queued.
     - Configure ``logs.s3`` on every controller.
   * - No live log tail across controllers, stored lines still arrive
     - The owning controller's internal URL is unreachable or the cluster
       secrets differ.
     - Check ``controller.internal_url``, the network between controllers on
       the internal port, and the secrets.

Apply and API
-------------

.. list-table::
   :header-rows: 1
   :widths: 28 32 40

   * - Symptom or message
     - Cause
     - Fix
   * - 409 ``managed by files:main``
     - The job or project belongs to a source (``damson apply``); the UI and API
       cannot change it.
     - Change the files and apply again, or adopt/release it
       (:doc:`/reference/jobs-as-code`).
   * - 409 ``archived``
     - The job was archived by ``apply --prune``.
     - Apply the file again to restore it.
   * - ``apply`` exits 1 with validation issues
     - The files fail validation; all issues are printed.
     - Fix them; run ``--dry-run`` first.
   * - ``apply`` 409 on a plan
     - A concurrent change won.
     - Run it again.
   * - 401 on the API
     - Missing or invalid token. With OIDC, anonymous access is off.
     - Send ``Authorization: Bearer ...`` or log in. Use the problem+json
       ``request_id`` to find the log line.

High database load
------------------

- The ``db`` log store writes every log chunk to PostgreSQL. Move logs to S3
  (:doc:`/reference/log-store`) when runs produce a lot of output.
- Too many connections: the sum of all controllers' pools exceeds
  ``max_connections``. Lower ``pool_max_conns`` in the DSN or add a pooler in
  session mode.
- Audit events and old runs are never pruned by default; set
  ``audit.retention_days`` and ``runs.retention_days``. Run pruning logs
  ``pruned finished runs`` at INFO on the controller holding the ``retention``
  lease; a large backlog is cleared over several hourly passes (each pass
  runs for up to half an hour, in batches of up to 20 runs). Each batch writes
  one ``run.prune`` audit event.
- Long idle transactions are cut after 30 seconds by design
  (``idle_in_transaction_session_timeout``).
- Many agents in a pool with a plugin none of them offers make the controllers
  requeue the same executions every 5 seconds (a known limitation of
  incapable agents). Fix the plugin or the agent set.
