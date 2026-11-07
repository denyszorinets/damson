buildah plugin
==============

The ``buildah`` plugin builds a container image from a git repository and pushes
it to a registry, reporting the registry digest. It is a built-in plugin in
``plugins/buildah``, written against the public plugin SDK only
(:doc:`/development/plugin-sdk`). Design: ARCHITECTURE section 6.5.

.. list-table::
   :widths: 25 75

   * - Manifest name
     - ``buildah``
   * - Target mode
     - ``none`` (it runs on the agent itself)
   * - Requires
     - ``bin:buildah``, ``bin:git``
   * - Idempotent
     - no

.. warning::

   A Containerfile is arbitrary code. Run this plugin only in a dedicated build
   pool (``runs_on.pool``) whose agents hold nothing else of value. Rootless
   buildah needs user namespaces and ``fuse-overlayfs`` (or
   ``STORAGE_DRIVER=vfs``); set such options in the agent process's environment
   (``BUILDAH_*``, ``CONTAINERS_*``, ``STORAGE_*`` variables are passed on).

Input
-----

.. list-table::
   :widths: 25 75
   :header-rows: 1

   * - Field
     - Meaning
   * - ``repo``
     - ``url``, ``ref``, ``credential`` and ``submodules`` as for the
       :doc:`ansible plugin <plugin-ansible>` (the shared checkout).
   * - ``context``
     - Build context directory in the repository, default ``.``.
   * - ``containerfile``
     - Path in the repository. Default: ``Containerfile`` in the context, else
       ``Dockerfile``.
   * - ``image``
     - The image **repository**, such as ``registry.internal/team/app``. No
       scheme, tag or digest (use ``tags``).
   * - ``tags``
     - Tags to apply. Default: the short (12 character) commit SHA.
   * - ``build_args``, ``labels``, ``target``, ``platform``
     - ``--build-arg``, ``--label``, ``--target`` and ``--platform`` (one
       platform). Build arguments are visible in the build output and the image
       history, so a value that is a ``secret://`` ref is refused: use
       ``build_secrets``.
   * - ``build_secrets``
     - Map of secret id to ``secret://`` ref. Each is written to a 0600 file and
       passed as ``--secret id=<id>,src=<file>``; use it in the Containerfile with
       ``RUN --mount=type=secret,id=<id>``.
   * - ``registry_credentials``
     - List of ``{registry, credential}``. ``credential`` is a ``password``
       secret (``username`` and ``password``). They are written to a registry
       auth file (0600, ``REGISTRY_AUTH_FILE``) used for pulling base images and
       pushing.
   * - ``cache``
     - ``{from, to}`` image repositories for ``--cache-from`` and ``--cache-to``.
   * - ``push``
     - Default ``true``.
   * - ``tls_verify``
     - Default ``true``. ``false`` is refused unless the operator sets
       ``agent.allow_insecure_registry``.

.. note::

   ARCHITECTURE section 6.5 describes ``registry_credentials`` as refs "of type
   ``registry``". There is no such secret type; the plugin uses explicit
   ``{registry, credential}`` pairs with the existing ``password`` type, which
   also says which registry a login is for.

Execution
---------

#. Credentials are resolved first: a missing secret fails the step before any
   clone.
#. The repository is checked out (the shared helper: depth 1, a token only for
   the repository's host, no use of the agent user's ssh keys or config).
#. ``buildah build --layers`` runs in the checkout with all tags and with
   ``--root``/``--runroot`` inside the work directory, so no image or layer
   stays on the agent.
#. Each tag is pushed with ``buildah push --digestfile``; the digests of all tags
   must agree.
#. ``buildah rmi --all --force`` runs even after a failure or a cancel, and the
   credential files are removed.

Buildah gets a minimal environment, not the agent's own (``HOME``, ``PATH``,
proxy and TLS variables and the ``BUILDAH_``, ``CONTAINERS_`` and ``STORAGE_``
families pass through). No secret is on a command line.

Output and errors
-----------------

``image``, ``digest`` (``sha256:...``, the registry digest), ``image_ref``
(``<image>@<digest>``, preferred for deploys), ``tags`` and ``commit``. With
``push: false`` there is no registry digest, so ``digest`` and ``image_ref``
are empty. A failed step still returns ``image``, ``tags`` and ``commit``.

Registry authentication failures are ``auth``, an unreachable registry or
network is ``connection``, and other build or push failures are ``exit_code``.
The buildah output is in the step log.
