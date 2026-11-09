.. _reference-helm:

Helm chart for Kubernetes
=========================

The chart in :repo:`deploy/helm/damson` installs the Damson controller as a
Deployment with two or more replicas and one agent Deployment per pool. It needs
PostgreSQL, which you run yourself. The chart never creates or stores a
credential: every secret value is read from a Kubernetes Secret that you create
and name in the values (``existingSecret``).

What the chart ships:

- Controller Deployment (default 2 replicas) with a rolling update of
  ``maxUnavailable: 0`` and ``maxSurge: 1``, a soft pod anti-affinity by node,
  a PodDisruptionBudget (``minAvailable: 1``), readiness, liveness and startup
  probes on the health endpoints, and a ``ClusterIP`` Service on port 8080.
- Each controller advertises its pod IP as its internal URL
  (``DAMSON_CONTROLLER__INTERNAL_URL=http://$(POD_IP):8081``) for
  controller-to-controller requests.
- One agent Deployment per entry in ``agents.pools``, with an optional
  HorizontalPodAutoscaler and PodDisruptionBudget, and a ServiceAccount of its
  own.
- A NetworkPolicy (on by default) and an optional Ingress.

The controller configuration is built from the values as a typed dict and
rendered with ``toToml`` into the ``config.toml`` ConfigMap. ``values.schema.json``
type-checks the values and sets ``additionalProperties: false``, so a misspelled
value name fails ``helm install``. It also restricts ``secrets.backend``,
``logs.store`` and ``secrets.openbao.auth.method`` to their allowed values and
requires ``agents.controllerUrl`` to match ``ws://`` or ``wss://``.

Prerequisites
-------------

- Kubernetes 1.27 or later, and ``helm`` 3. IPv4 or dual-stack with IPv4 as the
  primary family. IPv6-only clusters are not supported: the internal URL uses
  the pod IP (``http://$(POD_IP):8081``) and the controller rejects that form
  for an IPv6 address.
- A reachable PostgreSQL 13 or later database and user. Controllers need
  ``CREATE`` rights on the schema because they run migrations.
- Images: ``damson`` for the controller and ``damson-agent`` (or
  ``damson-agent-build``) for agents, pushed to a registry the cluster can pull
  from (see :doc:`images`). Set ``image.repository``, ``agents.image.repository``
  and the tags.
- The Secrets in the table below. Create only the ones your configuration uses.

.. list-table::
   :header-rows: 1
   :widths: 30 40 30

   * - Secret content
     - Requirements
     - Values that name it
   * - PostgreSQL DSN
     - ``postgres://user:pass@host/db?sslmode=verify-full``
     - ``database.existingSecret``, ``database.key`` (``dsn``)
   * - Cluster secret
     - At least 32 bytes, random. Always required.
     - ``secrets.clusterSecret.existingSecret``, ``.key`` (``cluster-secret``)
   * - KEK
     - Base64 of 32 bytes. Required with the ``db`` secret backend.
     - ``secrets.kek.existingSecret``, ``.key`` (``kek``)
   * - Pool tokens
     - One per pool, from ``damson admin token create``.
     - ``agents.pools[].tokenSecret``, ``.tokenKey`` (``token``)
   * - OIDC client secret
     - Required when ``oidc.issuer`` is set.
     - ``oidc.existingSecret``, ``oidc.clientSecretKey`` (``client-secret``)
   * - S3 credentials
     - Keys ``access-key-id`` and ``secret-access-key``. Leave unset to use the
       pod's cloud identity.
     - ``logs.s3.existingSecret``
   * - OpenBao credentials
     - A token (method ``token``, key ``tokenKey``) or role id and secret id
       (method ``approle``, keys ``roleIdKey`` and ``secretIdKey``). Required
       for those two methods; not used for method ``kubernetes``.
     - ``secrets.openbao.auth.existingSecret``

Install
-------

#. Create the namespace and the Secrets. ``damson admin kek generate`` prints a
   new KEK; ``openssl`` is one way to make the other random values.

   .. code-block:: console

      $ kubectl create namespace damson
      $ kubectl -n damson create secret generic damson-db \
          --from-literal=dsn='postgres://damson:CHANGE_ME@pg.example.com/damson?sslmode=verify-full'
      $ kubectl -n damson create secret generic damson-cluster \
          --from-literal=cluster-secret="$(openssl rand -base64 48)"
      $ kubectl -n damson create secret generic damson-kek \
          --from-literal=kek="$(openssl rand -base64 32)"

#. Write a values file. The ``agents.pools[].tokenSecret`` Secrets do not exist
   yet; the chart renders without them and the agent pods wait until they do
   (step 5).

   .. code-block:: yaml

      # values.yaml
      image: {repository: registry.example.com/damson, tag: v1.0.0}
      database: {existingSecret: damson-db}
      secrets:
        clusterSecret: {existingSecret: damson-cluster}
        kek: {existingSecret: damson-kek}
      ingress:
        enabled: true
        className: nginx
        hosts:
          - host: damson.example.com
            paths: [{path: /, pathType: Prefix}]
        tls: [{secretName: damson-tls, hosts: [damson.example.com]}]
      agents:
        controllerUrl: wss://damson.example.com
        image: {repository: registry.example.com/damson-agent, tag: v1.0.0}
        pools:
          - {name: default, replicas: 2, tokenSecret: pool-default}

#. Install:

   .. code-block:: console

      $ helm install damson deploy/helm/damson -n damson -f values.yaml

   The first controller to start migrates the database (see Upgrade); the
   others wait for it. Check with ``kubectl -n damson rollout status
   deploy/damson-controller``.

#. Create the admin token and the pools. ``damson admin`` talks to the database
   directly (:ref:`cli-admin`), so run it inside a controller pod, which already
   has ``DAMSON_DATABASE__DSN`` in its environment:

   .. code-block:: console

      $ kubectl -n damson exec deploy/damson-controller -- \
          damson admin admin-token create
      $ kubectl -n damson exec deploy/damson-controller -- \
          damson admin pool create default
      $ kubectl -n damson exec deploy/damson-controller -- \
          damson admin token create --pool default --name k8s-default

   Each token is printed once. Store the admin token in your password manager.

#. Put the pool token in the Secret named by ``tokenSecret``. The agents
   start as soon as it exists (restart them if they already failed to start):

   .. code-block:: console

      $ kubectl -n damson create secret generic pool-default \
          --from-literal=token='<token printed above>'

#. Open ``https://damson.example.com``. The agent pods log that they connected.

The exec target is the Deployment name ``<release fullname>-controller``; with
the release name ``damson`` it is ``damson-controller`` (a release name that
does not contain the chart name gets the prefix ``<release>-damson``; use
``kubectl get deploy`` to see it).

Upgrade
-------

.. code-block:: console

   $ helm upgrade damson deploy/helm/damson -n damson -f values.yaml

- Back up the database before every upgrade, and keep the KEK Secret. Migrations
  are forward only.
- The controller runs with ``--auto-migrate`` (``controller.autoMigrate``,
  default true). Migrations run at start behind a database advisory lock, so
  concurrent controllers apply them once and the others wait.
- A controller answers ``/readyz`` with 503 until the schema is migrated and the
  database is reachable, so the Service sends no traffic to a pod that is not
  ready.
- The rolling update has ``maxUnavailable: 0`` and ``maxSurge: 1``: a new pod
  must be ready before an old one is stopped. A config change rolls the pods
  (the ConfigMap checksum is a pod annotation).
- Migrations run before the listener opens, so the startup probe polls every
  2 s for ``controller.startupFailureThreshold`` attempts (default 300, ten
  minutes). Raise it for a long migration. Readiness and liveness probes have a
  ``timeoutSeconds`` of 5.
- The first new pod migrates while the old pods keep serving, and ``/readyz``
  only checks that migrations ran, not that they are compatible with the old
  code. Zero-downtime upgrades therefore need migrations that old controllers
  tolerate (expand/contract). Otherwise the old pods can return errors or be
  briefly unavailable during the roll. Take a database backup first.
- Agents roll one pool at a time. On SIGTERM an agent drains for
  ``agent.drain_grace`` (default 20 s) and then cancels what is still running.
  This also holds for node drains and HPA scale-down. For longer steps raise
  ``agent.drain_grace`` and the pool's ``terminationGracePeriodSeconds`` together. A protocol change stays compatible with the previous
  protocol version, so controllers and agents may differ by one release during
  the roll (:doc:`protocol`).
- Changing the content of a Secret (cluster secret, KEK, DSN, pool tokens) does
  not restart the pods, because the chart does not see the content. Run
  ``kubectl -n damson rollout restart deploy/damson-controller`` (and the agent
  Deployments) after you change one.

Set ``controller.autoMigrate: false`` only if you migrate by another means.

Values reference
----------------

``values.schema.json`` validates the values. Required values fail at render time
with a message that names the value. Full defaults are in
:repo:`deploy/helm/damson/values.yaml`.

.. list-table::
   :header-rows: 1
   :widths: 25 75

   * - Group
     - Contents and defaults
   * - ``nameOverride``, ``fullnameOverride``
     - Empty. Override resource names.
   * - ``image``
     - ``repository: damson``, ``tag`` empty (the chart ``appVersion``),
       ``pullPolicy: IfNotPresent``. ``imagePullSecrets: []``.
   * - ``database``
     - ``existingSecret`` (required), ``key: dsn``.
   * - ``secrets.clusterSecret``
     - ``existingSecret`` (required), ``key: cluster-secret``.
   * - ``secrets.backend``
     - ``db`` (default) or ``openbao``.
   * - ``secrets.kek``
     - ``existingSecret`` (required for ``db``), ``key: kek``.
   * - ``secrets.openbao``
     - ``address``, ``namespace``, ``mount: secret``, ``prefix: damson``,
       ``readOnly: false``, ``allowPlainHttp: false``, ``cacheTtl: 0s``,
       ``caSecret``/``caKey: ca.crt``, and ``auth`` with ``method: kubernetes``
       (``token``, ``approle``, ``kubernetes``), ``role: damson``, ``mount``,
       ``roleId``, ``existingSecret``, ``tokenKey: token``,
       ``secretIdKey: secret-id``.
   * - ``logs``
     - ``store: db`` (or ``s3``) and ``s3`` with ``endpoint``, ``region``,
       ``bucket``, ``prefix``, ``pathStyle: false``, ``allowPlainHttp: false``,
       ``sse``, ``existingSecret``, ``accessKeyIdKey``, ``secretAccessKeyKey``.
   * - ``oidc``
     - ``issuer`` (empty keeps anonymous access), ``clientId``,
       ``redirectUrl``, ``scopes: [profile, email, groups]``,
       ``groupsClaim: groups``, ``adminRule``, ``existingSecret``,
       ``clientSecretKey: client-secret``.
   * - ``auth``, ``audit``, ``logLevel``
     - ``auth.requireToken: false``, ``audit.retentionDays: 0``,
       ``logLevel: info``.
   * - ``controller``
     - ``replicas: 2``, ``autoMigrate: true``, ``resources`` (requests 100m CPU
       and 128Mi, limit 512Mi memory), ``podDisruptionBudget`` (enabled,
       ``minAvailable: 1``), ``terminationGracePeriodSeconds: 30``,
       ``podAnnotations``, ``podLabels``, ``nodeSelector``, ``tolerations``,
       ``affinity`` (default is soft anti-affinity by node),
       ``topologySpreadConstraints``, ``priorityClassName``, ``extraEnv``,
       ``config`` (nested map deep-merged over the generated ``config.toml``,
       for example ``{engine: {finally_grace: 3m}}``; no secrets; replaces the
       former ``extraConfig``), ``startupFailureThreshold: 300``,
       ``service`` (``ClusterIP``, port 8080), ``internalPort: 8081``.
   * - ``serviceAccount``
     - The controller's ServiceAccount: ``create: true``, ``annotations``,
       ``name``. Put cloud identity annotations (IRSA, workload identity for the
       S3 log store) here and nowhere else.
   * - ``ingress``
     - ``enabled: false``, ``className``, ``annotations``, ``hosts``
       (``damson.example.com``), ``tls: []``.
   * - ``trustForwardedProto``
     - ``false``. Used only when the ingress is enabled; ``ingress.tls`` also
       turns it on.
   * - ``networkPolicy``
     - ``enabled: true``, ``apiFrom: []``.
   * - ``agents``
     - ``controllerUrl`` (``ws://`` or ``wss://``; defaults to the first ingress
       host when the ingress is enabled, otherwise required), ``caSecret``,
       ``caKey: ca.crt``, ``image`` (``damson-agent``), ``serviceAccount``
       (``create: true``, ``name``, ``annotations``), ``spoolSizeLimit: 1Gi``
       and ``pools``.

Agent pools
-----------

Each entry in ``agents.pools`` becomes a Deployment named
``<fullname>-agent-<pool name>`` with the label ``damson.dev/pool``. The pool
itself must exist in Damson (``damson admin pool create``), and
``tokenSecret`` must be a Secret holding a token of that pool.

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - Pool field
     - Meaning (default)
   * - ``name``
     - Pool name, also the ``DAMSON_AGENT__POOL`` value.
   * - ``replicas``
     - Agents in the pool (2); 0 is honored. Ignored when autoscaling is
       enabled.
   * - ``slots``
     - Concurrent steps per agent (0, which takes the agent default).
   * - ``tokenSecret``, ``tokenKey``
     - Secret and key of the pool token (required; key ``token``).
   * - ``terminationGracePeriodSeconds``
     - 30. Running steps are cancelled when the pod stops, whatever this value
       is (see Upgrade); it does not give steps time to finish.
   * - ``resources``, ``image``, ``env``
     - Container resources; image override (``repository``, ``tag``); extra
       environment.
   * - ``nodeSelector``, ``tolerations``, ``affinity``
     - Scheduling.
   * - ``podSecurityContext``, ``securityContext``
     - Replace the restricted defaults (see Security context).
   * - ``extraVolumes``, ``extraVolumeMounts``
     - Added to the pod and container.
   * - ``autoscaling``
     - ``enabled: false``, ``minReplicas: 2``, ``maxReplicas: 10``,
       ``targetCPUUtilizationPercentage: 70``. Renders an HPA.
   * - ``podDisruptionBudget``
     - ``enabled: false``, ``maxUnavailable: 1``.

Agents connect to ``agents.controllerUrl`` (``wss://damson.example.com``). If the
controller certificate is signed by a private CA, put the bundle in a Secret
(key ``ca.crt``) and set ``agents.caSecret``; it is mounted and passed as
``DAMSON_AGENT__TLS_CA_FILE``. The chart does not set
``DAMSON_AGENT__CONTROLLER_CA_FILE``, which a private CA for a natively
serving controller needs; the same Secret is mounted at ``/etc/damson/ca``, so
add ``env: [{name: DAMSON_AGENT__CONTROLLER_CA_FILE, value: /etc/damson/ca/ca.crt}]``
to the pool.

Pool names must be unique, and a pool with autoscaling needs
``resources.requests.cpu``; both fail the render otherwise. The log spool is an
``emptyDir`` limited by ``agents.spoolSizeLimit`` (1Gi).

Agents run job code, so they get their own ServiceAccount
(``agents.serviceAccount``, created by default, no annotations) and no token is
mounted in agent pods. Annotations for IRSA or workload identity belong on
``serviceAccount.annotations`` (the controller) only: job code on an agent must
never inherit the controller's cloud identity, such as access to the log
bucket. Do not annotate the agent ServiceAccount unless a pool's jobs need that
identity.

The build pool below is a starting point to verify in your cluster, not a tested
recipe. Rootless buildah in a restricted pod needs overrides (user namespaces
and setuid ``newuidmap``/``newgidmap``), and ``HOME`` is ``/tmp`` in agent pods,
so container storage goes under ``/tmp/.local/share/containers``. See
:doc:`plugin-buildah` for the requirements:

.. code-block:: yaml

   agents:
     pools:
       - name: build
         replicas: 1
         tokenSecret: pool-build
         image: {repository: registry.example.com/damson-agent-build, tag: v1.0.0}
         resources: {requests: {cpu: "1", memory: 1Gi}}
         extraVolumes: [{name: containers, emptyDir: {}}]
         extraVolumeMounts:
           - {name: containers, mountPath: /tmp/.local/share/containers}
         # Add the securityContext / podSecurityContext overrides your cluster
         # needs for buildah (user namespaces, setuid newuidmap) for this pool only.

Ingress and WebSockets
----------------------

Agents hold a WebSocket to the controller, and the web UI uses long-lived event
streams, so the ingress must allow long idle connections. For ingress-nginx:

.. code-block:: yaml

   ingress:
     enabled: true
     className: nginx
     annotations:
       nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
       nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"

Other ingress controllers have their own timeout settings; raise the idle
timeout there too.

TLS and trustForwardedProto
---------------------------

The controller refuses plain ``ws://`` agent connections unless a proxy that
terminates TLS sets ``X-Forwarded-Proto: https`` (``controller.trust_forwarded_proto``,
:doc:`configuration`). The chart sets it only when the ingress is enabled and
either ``ingress.tls`` is set or ``trustForwardedProto: true``. Without an
ingress it stays off.

Anyone who can reach the controller pod around the ingress can forge the header.
The effect is small: the forger can only make its own connection, and so its own
token, travel in clear. Other clients are not affected. Still, restrict the API
port with ``networkPolicy.apiFrom`` so that only the ingress controller (and
agents) reach it. If TLS is terminated elsewhere (a load balancer in front of a
plain Service), set ``trustForwardedProto: true`` together with the ingress and
make sure that proxy sets the header.

Native TLS on the controller
----------------------------

``controller.tls.secretName`` names a ``kubernetes.io/tls`` Secret (``tls.crt``
with the chain, ``tls.key``) in the release namespace. The chart mounts it at
``/etc/damson/tls``, sets ``controller.tls_cert_file`` and
``controller.tls_key_file`` in the config, and switches the probes to scheme
HTTPS. The controller serves HTTPS and WSS and reloads the files when the Secret
changes (a cert-manager renewal; the kubelet refreshes the mounted files after a
delay, see :doc:`/operations/install`). Empty, the default, serves plain HTTP for an
ingress that terminates TLS.

An ingress in front of an HTTPS controller must speak HTTPS to the backend. For
ingress-nginx add ``nginx.ingress.kubernetes.io/backend-protocol: HTTPS`` to
``ingress.annotations`` (``ci/full.yaml`` does). ``agents.controllerUrl``
stays required.

The internal port stays plain HTTP in either case (next section).

NetworkPolicy and the internal port
-----------------------------------

Controllers call each other on ``controller.internalPort`` (8081) over plain
HTTP, authenticated by the cluster secret. That port must stay inside the
controller pods: no Service, Ingress or other client may reach it.

``networkPolicy.enabled`` is true by default. The chart then creates one policy
for the controller pods that allows:

- port 8081 from other controller pods only;
- port 8080 from anywhere, unless ``networkPolicy.apiFrom`` is set, in which
  case only from those sources (add the ingress controller namespace and the
  agents there):

  .. code-block:: yaml

     networkPolicy:
       enabled: true
       apiFrom:
         - namespaceSelector:
             matchLabels: {kubernetes.io/metadata.name: ingress-nginx}

The policy only restricts ingress to the controllers; it needs a CNI that
enforces NetworkPolicy. When you set ``apiFrom``, agent pods are not allowed
implicitly: list their namespace or pod selector too, as well as agents outside
the cluster and other API clients.

Security context
----------------

Pods follow the restricted Pod Security profile: ``runAsNonRoot``, the
``RuntimeDefault`` seccomp profile, no privilege escalation, all capabilities
dropped and a read-only root file system. Writable paths are ``emptyDir``
volumes (``/tmp``, and ``/var/spool/damson`` for agents). The controller runs
as uid 65532 and the agents as uid 10001, matching the images
(``deploy/docker/Dockerfile``). A pool may replace both security contexts, for
example for buildah.

Secrets handling
----------------

- The chart stores no credential. The ConfigMap holds only non-secret settings;
  credentials reach the pods as environment variables from ``secretKeyRef`` or
  as mounted Secret files. ``make helm-check`` fails if a rendered ConfigMap
  contains one.
- Create Secrets outside Helm (or with your secret manager, such as External
  Secrets or Sealed Secrets) so that they are not in release history.
- **Losing the KEK loses every secret stored with the ``db`` backend.** Back it
  up separately from the database. See :ref:`reference-config-kek` for rotation.
- The admin token and pool tokens are shown once; Damson stores only hashes.

Login, log store and OpenBao
----------------------------

OIDC (see :doc:`authentication`):

.. code-block:: yaml

   oidc:
     issuer: https://idp.example.com/realms/damson
     clientId: damson
     redirectUrl: https://damson.example.com/auth/callback
     adminRule: "'damson-admins' in claims.groups"
     existingSecret: damson-oidc   # key client-secret

S3 log store (see :doc:`log-store`):

.. code-block:: yaml

   logs:
     store: s3
     s3: {endpoint: https://s3.example.com, bucket: damson-logs, existingSecret: damson-s3}

OpenBao secret backend (see :doc:`secrets-openbao`); the KEK is then not needed.
With ``approle`` the role id is read from the auth Secret (``roleIdKey``),
mounted at ``/etc/damson/openbao-auth/role-id`` and set as ``role_id_file``; the
secret id reaches the pod as ``DAMSON_OPENBAO_SECRET_ID``. With ``token`` the
token is mounted and set as ``token_file``. ``auth.existingSecret`` is required
for ``approle`` and ``token``. The controller ServiceAccount token is mounted
only for the ``kubernetes`` method:

.. code-block:: yaml

   secrets:
     backend: openbao
     openbao:
       address: https://bao.example.com:8200
       caSecret: bao-ca
       auth: {method: kubernetes, role: damson}

Offline rendering
-----------------

``helm template`` needs no cluster:

.. code-block:: console

   $ helm template damson deploy/helm/damson -f values.yaml > damson.yaml

The files in ``deploy/helm/damson/ci/`` (``minimal.yaml``, ``full.yaml``) are
complete examples. Use them as a starting point.

How the chart is tested
-----------------------

``make helm-check`` (needs ``helm`` on ``PATH``) runs
:repo:`deploy/helm/check.sh`. For every file in ``deploy/helm/damson/ci/`` it
runs ``helm lint --strict`` and ``helm template``, then checks that a Deployment
renders, that no credential is in the ConfigMap, that the agent and controller
ServiceAccounts differ, and that every container drops all capabilities and has
a read-only root. It then loads each rendered ``config.toml`` with
``config.Load`` (``TestHelmRenderedConfig`` in ``internal/config``), so the chart
cannot render a configuration the controller refuses. It also checks that
``replicas: 0`` stays 0, and that bad input fails with a message naming the
value: missing required values (database Secret, cluster secret, KEK,
controller URL, pool token, OIDC client id, OpenBao auth Secret), bad replicas,
bad backend, a misspelled value name, a non-``ws`` controller URL, duplicate pool
names, and autoscaling without a CPU request. The CI job ``helm`` runs the same target (:doc:`/development/ci`).
