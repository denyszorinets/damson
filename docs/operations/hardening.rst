Hardening
=========

A checklist for production. Each item names the setting; the reference pages
explain it. Design background: ARCHITECTURE section 18.

Transport security
------------------

- **Agents use wss.** Agents send their pool token on the connection. Plain
  ``ws://`` is refused unless the controller runs with
  ``controller.insecure_dev``. Terminate TLS in a proxy or ingress; the
  controller has no TLS settings of its own. Set
  ``controller.trust_forwarded_proto = true`` only if the proxy sets or strips
  ``X-Forwarded-Proto``, otherwise clients can forge it.
- **PostgreSQL** with ``sslmode=verify-full`` in the DSN.
- **https everywhere else:** the OIDC issuer, ``secrets.openbao.address``
  (https is required; verification cannot be disabled), ``logs.s3.endpoint``,
  and S3, FTPS and WinRM targets of the ``transfer`` and ``winrm`` plugins.
- **Internal listener.** ``controller.internal_listen`` (default ``:8081``) is
  plain HTTP and carries log lines between controllers, authenticated by an
  HMAC of the cluster secret. Reachable only from the other controllers: a
  firewall rule, a security group, or the chart's NetworkPolicy (on by default).
  Never route it through the public load balancer; the public port answers 404
  for ``/internal/``.
- Restrict the API port with ``networkPolicy.apiFrom`` on Kubernetes if only
  the ingress controller and the agents need it.

Flags that must be off in production
------------------------------------

All default to off. Check your configuration for any that is on.

.. list-table::
   :header-rows: 1
   :widths: 38 62

   * - Setting
     - Why
   * - ``controller.insecure_dev`` / ``--insecure-dev``
     - Accepts plain ``ws://`` agents; pool tokens cross the network in the
       clear. ``--dev-nodes`` on a controller also needs it.
   * - ``auth.cookie_insecure``
     - Drops ``Secure`` and the ``__Host-`` prefix from the session cookie.
   * - ``secrets.openbao.allow_plain_http``
     - Sends the OpenBao token over http.
   * - ``logs.s3.allow_plain_http``
     - Sends logs and S3 credentials over http.
   * - ``agent.allow_insecure_host_keys``
     - Lets plugins connect to nodes with host key policy ``insecure`` and
       clone ssh repositories without a known_hosts entry.
   * - ``agent.allow_insecure_winrm``
     - WinRM over http and without certificate verification.
   * - ``agent.allow_plain_ftp``
     - Clear text FTP with passwords.
   * - ``agent.allow_plain_s3``
     - Clear text S3 endpoints.
   * - ``agent.allow_insecure_git``
     - Clones from ``http://``, ``git://`` and ``file://``.
   * - ``agent.allow_insecure_registry``
     - Lets buildah push with ``tls_verify: false``.
   * - ``agent.allow_helm_in_cluster``
     - Lets steps deploy with the agent's own Kubernetes identity. Turn it on
       only for a pool that is meant to hold that authority.

Authentication and access
-------------------------

.. warning::

   ``auth.require_token`` defaults to **false**: without OIDC, the job, run and
   execution API accepts requests without a token. ARCHITECTURE planned a true
   default; ADR-0064 records the deviation. The controller logs ``auth.require_token
   is off`` at start. In production, either configure OIDC (anonymous access
   then ends) or set ``auth.require_token = true`` and use service account
   tokens. The web UI has no login without OIDC, so with ``require_token`` the
   job and run pages fail in the browser.

- **OIDC** (:doc:`/reference/authentication`): set ``auth.oidc.admin_rule``
  narrowly (for example ``'damson-admins' in user.groups``). Claims are fixed
  at login, so a role revoked at the provider lasts up to 12 hours (the session
  lifetime). Serve the site over HTTPS, because the session cookie is
  ``Secure``.
- **Access rules** (:doc:`/reference/access-rules`): give people the lowest role
  that fits, per project. Instance admin is a separate verdict.
- **Service accounts** (:doc:`/reference/service-accounts`): one account per
  automation, a token per use, the lowest role (``runner`` before ``editor``),
  scoped to one project, with ``expires_at``. Tokens are never admin.
- **Admin token.** Treat the ``dma_`` token as a break-glass credential; rotate
  it when an operator leaves (:doc:`upgrade`).
- **Audit.** Every mutation writes an event (:doc:`/reference/audit`). Decide
  on ``audit.retention_days`` (default keep forever) and copy events to your log
  system if you need them longer. ``cli:`` actors are labels, not proof of
  identity.

Secrets
-------

- Credentials only in environment variables or files of mode 0600: the DSN,
  cluster secret, KEK, OIDC client secret, S3 keys, OpenBao token. Not in the
  TOML file, a unit file, a command line or a values file. The chart references
  Kubernetes Secrets by name.
- The KEK is separate from the database backup (:doc:`backup`). With OpenBao
  (:doc:`/reference/secrets-openbao`), give Damson's policy access to its
  prefix only, and prefer ``auth.method = kubernetes`` or ``approle`` to a
  long-lived token. ``secrets.openbao.read_only`` makes OpenBao the only place
  where secrets change.
- Secret values exist only on the dispatch path and in agent memory, and are
  masked in logs. An agent can read every secret dispatched to its steps, so
  scope secrets to projects and pools you trust.

Agents run job code
-------------------

Anything a job can do, the agent host or pod can do.

- Separate pools, tokens and hosts per trust boundary. Pin jobs to pools with
  ``runs_on.pool`` so that sensitive jobs only run on dedicated agents.
- Kubernetes: the chart gives every pool its own ServiceAccount, with no token
  mounted and no cloud identity, and distinct from the controller's. Keep it
  that way.
- VMs: keep the systemd unit hardening and loosen it only through a drop-in
  (:doc:`/reference/systemd`). The agent unit has no system call filter because
  it runs job tools.
- Do not link extra plugin modules into controller binaries
  (:ref:`cli-build`); extras are fully trusted code.
- The agent spool file holds masked job output in plaintext (mode 0600); put
  ``agent.spool_dir`` on a private directory.
- Host key policies: prefer ``strict`` with pinned keys; ``insecure`` is
  refused at dispatch without the agent flag, and ``tofu`` records the first
  key seen.

Binaries and images
-------------------

- Verify the checksum of the offline bundle with ``verify.sh`` and deliver the
  ``.sha256`` over another trusted channel than the archive. There is no
  signature yet (:doc:`/reference/offline-bundle`).
- Pull images by digest and mirror them into your registry. Image signing is
  an open decision, so there is nothing to verify beyond the digest.
- Build custom agent binaries yourself with ``damson build`` and pin module
  versions.

S3 log bucket
-------------

- A private bucket with public access blocked and a dedicated principal that
  has only ``s3:PutObject``, ``s3:GetObject``, ``s3:DeleteObject`` and
  ``s3:ListBucket`` on the bucket and prefix.
- Server-side encryption (``logs.s3.sse``). Logs are already masked by the
  agent, but they can still hold sensitive output.
- Prefer a cloud identity (IRSA, instance role) to static keys.

Network summary
---------------

.. list-table::
   :header-rows: 1
   :widths: 30 30 40

   * - From
     - To
     - Port and protocol
   * - Users, agents
     - Controllers (through the proxy)
     - 443 https and wss
   * - Proxy
     - Controllers
     - 8080 http
   * - Controllers
     - Controllers
     - 8081 http (private network)
   * - Controllers
     - PostgreSQL
     - 5432 with TLS
   * - Controllers
     - OIDC provider, OpenBao, S3
     - 443 https
