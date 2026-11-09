Install
=======

The steps are the same on Kubernetes and on VMs. This page gives the order and
the shared prerequisites; the target guides give the exact files and
commands: :doc:`/reference/helm` and :doc:`/reference/systemd`.

Order of steps
--------------

#. Prepare PostgreSQL.
#. Generate the secrets.
#. Serve TLS: on the controllers or in front of them.
#. Install and start the controllers (they migrate the database).
#. Create the admin token, the agent pools and their tokens.
#. Install the agents.
#. Set up login.
#. Run a smoke test.

PostgreSQL
----------

Create an empty database and a role that owns it. Damson creates its own tables
(and River's job tables) at the first controller start; it needs the right to
create them, so do not use a read-only role. The code does not pin a
version; the test suite runs against PostgreSQL 17 through testcontainers, so
prefer a current release.

.. code-block:: console

   $ psql -h pg.example.com -U postgres -c "CREATE ROLE damson LOGIN PASSWORD 'CHANGE-ME'"
   $ psql -h pg.example.com -U postgres -c "CREATE DATABASE damson OWNER damson"

Settings that matter:

- **TLS.** Use a DSN with ``sslmode=verify-full`` (and ``sslrootcert=`` for a
  private CA). The DSN is ``database.dsn``, treated as a secret and never
  logged; pass it as ``DAMSON_DATABASE__DSN`` rather than in a TOML file.
- **Idle transactions.** The controller sets
  ``idle_in_transaction_session_timeout`` to 30 seconds on every connection
  unless your DSN sets it. A controller host that dies mid-transaction then
  cannot hold row locks for long. Leave the default
  (:ref:`reference-config`).
- **TCP keepalives.** ARCHITECTURE section 16 recommends the server settings
  ``tcp_keepalives_idle``, ``tcp_keepalives_interval`` and
  ``tcp_keepalives_count`` so that dead peers are noticed. Set them, or rely on
  your managed service's defaults.
- **Connections.** Each controller holds a connection pool (the pgx default
  unless the DSN sets ``pool_max_conns``; the code enforces at least 2).
  Add up the pools of all controllers plus your own tools and keep the total
  under ``max_connections``. No extension is needed. If you put a connection
  pooler in front, use session pooling: the migration holds a session advisory
  lock.

.. note::

   SQLite works for ``damson dev`` and for one controller. A second controller
   on the same SQLite file refuses to start. Use PostgreSQL for anything with
   more than one controller.

Secrets to generate
-------------------

.. list-table::
   :header-rows: 1
   :widths: 22 40 38

   * - Secret
     - Command
     - Where it goes
   * - KEK
     - ``damson admin kek generate``
     - ``DAMSON_KEK`` on every controller. Not needed with the ``openbao``
       secret backend.
   * - Cluster secret
     - ``openssl rand -base64 48``
     - ``DAMSON_CONTROLLER__CLUSTER_SECRET`` on every controller. At least 32
       bytes; required with PostgreSQL.
   * - Database DSN
     - from your database
     - ``DAMSON_DATABASE__DSN``
   * - Admin token
     - ``damson admin admin-token create``
     - Your password manager. Printed once.
   * - Pool tokens
     - ``damson admin token create --pool NAME --name TOKEN``
     - ``DAMSON_AGENT__TOKEN`` of the agents of that pool. Printed once.
   * - OIDC client secret
     - from your identity provider
     - ``DAMSON_AUTH__OIDC__CLIENT_SECRET`` (or ``client_secret_file``)

Keep the KEK out of the database backup and out of version control. Losing it
loses every secret in the ``db`` backend (:doc:`backup`). All controllers must
hold the same KEK and the same cluster secret; only
``controller.internal_url`` differs per controller.

Credentials go in environment variables or files of mode 0600, never in the
TOML file (:doc:`hardening`).

TLS
---

There are two supported ways to serve TLS; pick one.

**Native TLS.** Set ``controller.tls_cert_file`` (leaf certificate followed by
the chain, PEM) and ``controller.tls_key_file`` (PEM) in ``controller.toml``, or
``DAMSON_CONTROLLER__TLS_CERT_FILE`` and ``DAMSON_CONTROLLER__TLS_KEY_FILE``.
Both or neither: one without the other is a startup error. The listener
``controller.listen`` then serves HTTPS and WSS (API, UI and agent sockets) with
TLS 1.2 as the minimum, ALPN ``h2`` and ``http/1.1`` (the agent WebSocket upgrade uses HTTP/1.1). Only
the ``controller`` role accepts these keys; ``dev`` refuses them. Unreadable
or mismatched files stop the controller at start. The key file is a secret: on
systemd use mode 0640, owner ``root:damson``; in Kubernetes use a
``kubernetes.io/tls`` Secret (:doc:`/reference/helm`). Client-certificate
authentication, OCSP stapling and ACME are not supported.

**TLS-terminating proxy.** The controller serves plain HTTP on
``controller.listen`` (default ``:8080``) and a load balancer, reverse proxy or
ingress terminates TLS and forwards HTTP and WebSocket upgrades to the
controllers. This stays supported.

*Renewal.* The controller checks the certificate files at most every 10 seconds
(on a TLS handshake), by modification time and size. A changed pair is loaded
without a restart and logged as ``controller tls certificate reloaded``. A broken
replacement is ignored: the previous certificate keeps serving and one ERROR,
``controller tls certificate not reloaded; still serving the previous one``, is
logged per change. Replace the certificate and the key together, as cert-manager
and certbot do.

Further reload behaviour:

- Files that become unreadable or are removed log one ERROR, ``controller tls
  files cannot be read; still serving the previous certificate``, per distinct
  error.
- A certificate that is expired or not yet valid logs the WARN ``controller tls
  certificate is not valid now`` at load and reload.
- A tool that replaces the certificate first and the key second (certbot) can
  cause one ``not reloaded`` ERROR between the two writes, followed by
  ``reloaded`` when the key lands. This is harmless.
- Change detection uses modification time and size. A same-size replacement that
  preserves the modification time (``cp -p``, ``rsync -a``) is not noticed until
  a restart or a ``touch``.
- Kubernetes Secret volumes refresh after a kubelet delay; this can take a
  minute or more.

*Private CA for agents.* Agents already connect with ``wss://`` and verify the
controller certificate; verification cannot be turned off. If the certificate
is signed by a private CA, or is self-signed, set ``agent.controller_ca_file``
(``DAMSON_AGENT__CONTROLLER_CA_FILE``) on the agents to a PEM file of CA
certificates. They are trusted in addition to the system roots. This is distinct
from ``agent.tls_ca_file``, which serves the ``transfer``, ``winrm`` and
``ansible`` plugins.

Agents connect to ``<controller_url>/agent/v1/connect`` and the controller
refuses a connection that is not secure, with the message ``plain ws:// is not
allowed``. A connection counts as secure when it arrived over TLS (including
a direct native TLS connection, which needs no other setting) or, with
``controller.trust_forwarded_proto = true``, when the proxy sent
``X-Forwarded-Proto: https``. Enable that option only behind a proxy that sets
or strips the header. The Helm chart's ingress and the systemd guide cover the
details.

Do not expose ``controller.internal_listen`` (default ``:8081``) outside the
controller network. It stays plain HTTP, also when native TLS is on (TLS covers
the main listener only), is authenticated by the cluster secret, and carries
log lines between controllers.

Controllers
-----------

Install the controllers as described in the target guide. Both guides start
them with ``--auto-migrate``: the first controller applies the migrations under
a database lock and the others wait. Until the database is migrated a
controller answers ``/healthz`` but ``/readyz`` returns 503
(:doc:`monitoring`).

Check that they are up:

.. code-block:: console

   $ curl -fsS https://damson.example.com/readyz
   {"status":"ready"}

Admin token, pools and tokens
-----------------------------

``damson admin`` talks to the database directly, so run it where the
controller's environment is available (a controller pod, or a transient
systemd unit; the target guides show how).

.. code-block:: console

   $ damson admin admin-token create
   $ damson admin pool create default
   $ damson admin token create --pool default --name prod-default

The admin token and the pool token are printed once. Only their hashes are
stored. See :ref:`cli-admin`.

Agents
------

Give each agent the controller URL (``wss://...``), the pool name and the pool
token. Use a separate pool, token and (on Kubernetes) ServiceAccount per trust
boundary, because agents run job code. The chart creates the agent Deployments
for you; on VMs install the unit on each host. Agents with missing tools offer
fewer plugins and log which ones they withhold (:doc:`troubleshooting`).

First login and OIDC
--------------------

Out of the box nobody logs in: the UI works without a login and the admin
pages ask for the admin token. For production configure OIDC
(:doc:`/reference/authentication`), with an ``admin_rule`` that makes your
operators instance admins, and access rules for projects
(:doc:`/reference/access-rules`). Once ``auth.oidc.issuer`` is set, anonymous
access ends. The admin token stays valid as the recovery token. If you do not
use OIDC, set ``auth.require_token = true`` and use service account tokens
(:doc:`/reference/service-accounts`); see :doc:`hardening`.

Smoke test
----------

#. The controllers are ready and the agent is connected:

   .. code-block:: console

      $ curl -fsS https://damson.example.com/readyz
      $ curl -fsS -H "Authorization: Bearer $DAMSON_ADMIN_TOKEN" \
          https://damson.example.com/api/v1/agents

   The agent's ``status`` is ``connected`` and ``pool_id`` names your pool.
   The same list is the **Agents** page of the UI.

#. Create a project, a node and a job as in :doc:`/tutorial/ssh-job` (in
   production the node and its ``secret://`` credential come from the
   inventory and secrets APIs, not from ``--dev-nodes``), or apply a folder
   of files (:doc:`/reference/jobs-as-code`).

#. Trigger the job and watch the run reach ``succeeded``, with its log in the
   run page. Trigger it again while you stop one controller to see another take
   over (:doc:`ha`).
