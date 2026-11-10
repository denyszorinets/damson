Container images
================

``deploy/docker/Dockerfile`` builds the images of ARCHITECTURE section 16 from
one source tree. Each is a target:

.. list-table::
   :widths: 25 75
   :header-rows: 1

   * - Target
     - Contents
   * - ``damson``
     - The static binary on a distroless base, running as a non-root user. Use
       it for the controller (``damson controller``) or any role.
   * - ``damson-agent``
     - The binary plus ``git``, ``openssh-client``, ``sshpass`` and a pinned
       ``ansible-core`` with ``pywinrm`` and the collections ``ansible.posix``
       and ``community.general``. Default command: ``agent``.
   * - ``damson-agent-build``
     - The binary plus ``git``, ``buildah`` (with ``fuse-overlayfs`` and
       ``uidmap``) and a pinned ``helm`` verified against a sha256. Default
       command: ``agent``. It sets ``STORAGE_DRIVER=vfs`` and
       ``BUILDAH_ISOLATION=chroot``, which work in an unprivileged pod.

Published images
----------------

Each release publishes the three targets to the GitHub Container Registry:
``ghcr.io/denyszorinets/damson``, ``ghcr.io/denyszorinets/damson-agent`` and
``ghcr.io/denyszorinets/damson-agent-build``. They are multi-arch manifests for
``linux/amd64`` and ``linux/arm64``. Tags: ``vX.Y.Z``, ``X.Y.Z`` and ``latest``
(pin a version in production)::

   $ docker pull ghcr.io/denyszorinets/damson-agent:v0.1.0

The images are not signed yet (:doc:`/operations/install`). The Helm chart
uses these repositories by default (:doc:`helm`).

Build your own
--------------

Build one with::

   docker build -f deploy/docker/Dockerfile --target damson-agent -t damson-agent .

Tool versions are build arguments (``ANSIBLE_CORE_VERSION``, ``HELM_VERSION``
with ``HELM_SHA256_AMD64`` and ``HELM_SHA256_ARM64``). The "Images" workflow
builds all three targets on changes to the Dockerfile, without pushing, and runs
each tool once. Add a custom plugin set with ``damson build --with`` on top of
either agent image (:ref:`cli-build`).

Capabilities and plugins
------------------------

At start the agent looks for ``ansible``, ``ansible-playbook``, ``buildah``,
``git``, ``helm`` and ``ssh`` on ``PATH`` and announces each as a capability
``bin:<name>`` in ``agent.hello``. A plugin lists what it needs in its manifest
(``requires``: ``ansible`` needs ``bin:ansible-playbook`` and ``bin:git``,
``buildah`` needs ``bin:buildah`` and ``bin:git``, ``helm`` needs ``bin:helm`` and
``bin:git``). **The agent offers a plugin only when every capability it requires
is present**, and logs a warning naming the plugins it withholds. A step for a
plugin no agent offers waits in the queue (it is not sent to an agent that would
fail it), so run those steps in a pool of the matching image. Extra capabilities
can be announced with the agent configuration.

Rootless buildah
----------------

Builds need user namespaces. In Kubernetes that usually means a relaxed
``securityContext`` for the build pool (for example ``runAsUser: 10001`` with
``seccompProfile: Unconfined`` and, on some clusters, ``appArmorProfile:
Unconfined``); do not run the build pool privileged. The image has the sub-uid
and sub-gid ranges ``100000:65536`` for the ``damson`` user. With ``/dev/fuse``
available set ``STORAGE_DRIVER=overlay`` for faster builds. A Containerfile is
arbitrary code: keep build agents in a dedicated pool
(:doc:`/reference/plugin-buildah`).
