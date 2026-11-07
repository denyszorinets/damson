helm plugin
===========

The ``helm`` plugin installs, upgrades, rolls back, uninstalls or inspects a Helm
release. It is a built-in plugin in ``plugins/helm``, written against the public
plugin SDK only (:doc:`/development/plugin-sdk`). Design: ARCHITECTURE section
6.6 and :doc:`/architecture/decisions/0057-helm-credentials`.

.. list-table::
   :widths: 25 75

   * - Manifest name
     - ``helm``
   * - Target mode
     - ``none`` (it runs on the agent itself)
   * - Requires
     - ``bin:helm``, plus ``bin:git`` for a git chart
   * - Idempotent
     - no

Input
-----

.. list-table::
   :widths: 25 75
   :header-rows: 1

   * - Field
     - Meaning
   * - ``action``
     - ``upgrade`` (install or upgrade; the default), ``rollback``,
       ``uninstall`` or ``status``.
   * - ``release``, ``namespace``, ``create_namespace``
     - Release name and namespace; ``create_namespace`` is for ``upgrade``.
   * - ``chart``
     - For ``upgrade``, exactly one of ``{oci, version}`` (an
       ``oci://registry/charts/app`` reference without a tag),
       ``{repo: {url, name, version}}`` (an https chart repository) or
       ``{git: {url, ref, path, credential}}`` (a chart directory in a git
       repository, through the shared checkout).
   * - ``values``
     - Chart values. A string ``secret://...`` anywhere in it is resolved from the
       secret bundle. This is the place for build outputs such as
       ``${{ steps.build.outputs.image_ref }}``.
   * - ``values_files``
     - Values files of a git chart, **relative to the chart directory**.
   * - ``set``
     - Single values as strings (``--set-string``). They are visible on the
       command line, so a ``secret://`` value is refused: use ``values``.
   * - ``cluster``
     - Exactly one of ``{kubeconfig, context}`` and ``{in_cluster: true}``.
   * - ``atomic``, ``wait``, ``timeout``
     - Default ``true``, ``true`` and ``10m``. Atomic makes helm roll back a
       failed upgrade by itself.
   * - ``registry_credentials``
     - ``{registry, credential}`` pairs with a ``password`` secret, for pulling
       an OCI chart (as for the :doc:`buildah plugin <plugin-buildah>`).
   * - ``rollback_revision``
     - For ``rollback``; the default is the previous revision.

The cluster
-----------

``cluster.kubeconfig`` is a ``secret://`` ref to the field ``kubeconfig`` of a
secret (``secret://team/kube-prod#kubeconfig``), or to a whole secret that has a
field of that name (a ``generic`` secret; there is no ``kubeconfig`` secret
type). It is written to a 0600 file and passed with ``--kubeconfig``; the agent's
own ``KUBECONFIG`` is never used.

``cluster.in_cluster`` deploys with the agent's own service account. That account
is the agent pool's authority, not a credential the job brought, so the step is
refused unless the operator sets ``agent.allow_helm_in_cluster``. Use it only in
a dedicated pool.

Execution and safety
--------------------

Credentials are resolved first, then a git chart is cloned (depth 1, a token only
for the repository's own host). The chart directory and every values file are
resolved through symlinks and must stay inside the checkout; helm gets absolute
paths. Helm runs with a minimal environment: its cache, config and data are in
the work directory, ``HOME`` is private, and only proxy and TLS variables pass
through (plus the API server address for ``in_cluster``). No secret is on a
command line. The files are removed when the step ends.

After ``rollback`` the plugin runs ``helm status`` to report the new revision.

Output and errors
-----------------

``release``, ``namespace``, ``revision``, ``status``, ``chart``, ``chart_version``
and ``app_version`` (from helm's JSON). After ``uninstall`` the status is
``uninstalled`` and the revision 0. A failed step has no outputs.

Authentication or authorization failures (``Unauthorized``, ``forbidden``) are
``auth``; an unreachable cluster or registry is ``connection``; helm timing out
while waiting for the release is ``timeout``; everything else is ``exit_code``.
Only helm's own ``Error:`` lines are read for this, not the output of hooks. With
``atomic`` a failed upgrade has already been rolled back when the step fails.
