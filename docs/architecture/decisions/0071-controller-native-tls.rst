ADR-0071: Controller-native TLS from files
==========================================

:Status: Accepted
:Date: 2026-11-09
:Amends: ARCHITECTURE sections 7.1 and 18 (the controller can serve TLS itself)

Issue :issue:`231`. Related: ADR-0040 (internal listener),
:doc:`0070-prometheus-metrics-endpoint`.

Context
-------

The controller served plain HTTP and relied on a proxy or ingress for TLS.
Small installs (a single host, systemd) then needed an extra component only to
get ``wss://`` and HTTPS.

Decision
--------

**Files.** ``controller.tls_cert_file`` (leaf and chain, PEM) and
``controller.tls_key_file`` (PEM). Both or neither; one alone is a startup
error. Unreadable or mismatched files stop the controller at start. With both
set, ``controller.listen`` serves HTTPS and WSS (API, UI, agent sockets). Only
the ``controller`` role accepts the keys; the ``dev`` role refuses them at
validation, because its in-process agent connects over ``ws://``.

**Library.** Standard library ``crypto/tls`` only; no new dependency.

**Protocol.** TLS 1.2 minimum. ALPN offers ``h2`` and ``http/1.1``. Browsers
and API clients get HTTP/2, which avoids the six-connection limit with several
SSE run pages open. The agent WebSocket upgrade runs over HTTP/1.1 (Go clients
ask for it on upgrade requests; a test covers it).

**Reload.** On a handshake, the files are re-checked at most every 10 seconds by
modification time and size. A changed pair is loaded without restart. A broken
replacement is ignored: the previous certificate keeps serving and one ERROR is
logged per distinct error. A certificate that is expired or not yet valid logs
a WARN at load and reload. Certificates are replaced together with their key, as
cert-manager and certbot do. A mounted Kubernetes Secret is refreshed by the
kubelet with a delay.

**Internal listener unchanged.** ``controller.internal_listen`` (log tail proxy,
HMAC with the cluster secret) stays plain HTTP. TLS covers the main listener
only; the internal port stays on the pod or host network.

**Agents.** Agents already used ``wss://``. A direct TLS connection counts as
secure, so ``trust_forwarded_proto`` is not needed. The new key
``agent.controller_ca_file`` adds CA certificates to the system roots for that
connection. It is additive only; verification cannot be turned off. It is
separate from ``agent.tls_ca_file`` (plugins).

**Deployment.** systemd: files in ``/etc/damson``, key mode 0640 ``root:damson``.
Helm: ``controller.tls.secretName``, a ``kubernetes.io/tls`` Secret mounted at
``/etc/damson/tls``; probes use HTTPS and the Service port gets
``appProtocol: https``. ``agents.caSecret`` also sets
``DAMSON_AGENT__CONTROLLER_CA_FILE``. A proxy that terminates TLS stays
supported.

Alternatives
------------

- **Proxy only (status quo).** Rejected as the sole option: it forces an extra
  component on small installs. It remains supported.
- **ACME in the controller.** Deferred. It needs challenge handling and
  storage shared between controllers, and certificates from files cover
  cert-manager and certbot.
- **Client certificates (mTLS).** Deferred. Pool tokens authenticate agents; no
  OCSP either.

Consequences
------------

- Operators choose native TLS or a proxy; docs cover both
  (:doc:`/operations/install`).
- Log lines between controllers still cross the network in clear text.
