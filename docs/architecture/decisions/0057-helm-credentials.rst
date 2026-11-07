ADR-0057: Helm credentials and in-cluster access
================================================

:Status: Accepted
:Date: 2026-11-07

Issue :issue:`183`. Clarifies ARCHITECTURE section 6.6; follows
:doc:`0056-buildah-registry-credentials`.

Context
-------

ARCHITECTURE 6.6 named a secret "of type ``kubeconfig``", which does not exist
(the types are ``generic``, ``password``, ``ssh_key`` and ``token``), and offered
``in_cluster`` (the agent's own service account) without saying who may use it.
``values_files`` and ``registry_credentials`` were also left open.

Decision
--------

- **Kubeconfig.** ``cluster.kubeconfig`` is a ``secret://`` ref to the field
  ``kubeconfig`` (``secret://team/kube-prod#kubeconfig``) or to a secret that has
  that field. No new secret type: a ``generic`` secret holds it.
- **In cluster.** ``cluster.in_cluster`` works only when the operator sets
  ``agent.allow_helm_in_cluster``. The service account is the pool's ambient
  authority; without the switch a job could use an identity it was never given.
- **A helm step is a security boundary, unlike an ansible play.** Go templates
  are otherwise sandboxed, so the boundary is cheap to keep: the kubeconfig must
  be self-contained (no ``tokenFile``, file paths, ``exec`` or ``auth-provider``;
  client-go would read the agent's disk or run programs), and a chart may not
  contain a symlink that resolves outside the checkout (helm follows chart
  symlinks, so ``.Files.Get`` could read the agent's service account token or
  other steps' files). Without these two checks the in-cluster gate would be
  bypassable by a job author.
- **Release readback.** The plugin reads the release with ``helm get metadata``
  (helm 3.13 or newer) instead of parsing ``--output json`` of the action: that
  output is one line holding the chart, the values and the manifests, which can
  exceed any log-line limit and which would put resolved secrets into the plugin.
- **Cancel.** A cancel sends SIGTERM and waits 60 seconds, so ``--atomic`` can
  roll back; only then is helm killed.
- **Values.** Strings of the form ``secret://...`` inside ``values`` are resolved
  into a 0600 values file. ``set`` is passed on the command line (``--set-string``)
  and therefore refuses secret refs.
- **Values files** of a git chart are relative to the chart directory and, like
  the chart path, must stay inside the checkout after symlinks are resolved.
- **Registry logins** for OCI charts use the ``{registry, credential}`` pairs of
  ADR-0056, written as a registry config file.

Alternatives Considered
-----------------------

- *A ``kubeconfig`` secret type.* Changes the secret schema and UI for one field.
- *Allowing ``in_cluster`` by default.* Rejected: ambient authority.
- *Passing secrets with ``--set``.* Rejected: visible in the process list.

Decision Criteria
-----------------

No schema change; no ambient authority by default; no secret on a command line.

Rationale
---------

A field reference reuses existing secrets and the existing field syntax. The
in-cluster switch follows the other agent gates (insecure host keys, plain ftp,
insecure registry).

Consequences
------------

Positive
~~~~~~~~

- Existing secrets work; the gate keeps the agent's identity out of reach of jobs.

Negative
~~~~~~~~

- A kubeconfig is a generic secret, so the UI cannot show its type.
- Kubeconfigs that rely on a token file, a certificate path or an exec plugin
  (typical for cloud CLIs) cannot be used; mint a token or inline the data.
- ``in_cluster`` needs an operator change before first use.

Risks
~~~~~

- A kubeconfig is powerful. The step that holds it is trusted code on the agent;
  use a dedicated pool for deploys.

Follow-up
~~~~~~~~~

- Basic-auth chart repositories are not supported (credentials would reach the
  command line).

Validation
----------

The helm tests check that no kubeconfig, value or registry password reaches a
command line or the environment, that nothing from helm's release object reaches
the outputs or the log, the in-cluster gate, the kubeconfig and chart-symlink
refusals, symlink confinement of the chart and values files, and the input
validation. The proc tests check the SIGTERM grace period.

References
----------

- ARCHITECTURE section 6.6
- :doc:`/reference/plugin-helm`
