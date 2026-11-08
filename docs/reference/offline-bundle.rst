.. _reference-offline-bundle:

Offline bundle
==============

The offline bundle is one archive with everything an air-gapped site needs to
install Damson: binaries, the Helm chart, the systemd files and, if you ask for
them, the container images. Nothing in it contacts the internet when it runs.

Contents
--------

.. code-block:: text

   damson-bundle-VERSION/
     bin/damson-linux-amd64, bin/damson-linux-arm64   static binaries
     chart/damson-*.tgz                               Helm chart
     systemd/                                         units, sysusers, examples
     images/*.tar                                     only with WITH_IMAGES=1
     SHA256SUMS                                       checksum of every file
     manifest.json                                    name, version, chart_version, images
     INSTALL.txt, LICENSE
     verify.sh, load-images.sh

Build
-----

.. code-block:: console

   $ make web
   $ make bundle                  # binaries, chart, units, checksums
   $ WITH_IMAGES=1 make bundle    # plus the three images (docker or podman, a few GB)

``make bundle`` runs :repo:`deploy/bundle/build.sh`. It needs Go, ``helm``,
``tar``, ``sha256sum`` and ``web/build`` (hence ``make web``). The output is
``dist/damson-bundle-VERSION.tar.gz`` and ``dist/damson-bundle-VERSION.tar.gz.sha256``.
``VERSION`` comes from ``git describe`` unless you set it. The archive is
reproducible: sorted names, owner 0, a fixed modification time. The images
are ``damson``, ``damson-agent`` and ``damson-agent-build`` built from
:repo:`deploy/docker/Dockerfile` (see :doc:`images`).

Verify
------

Check the archive before you unpack it, then every file after:

.. code-block:: console

   $ sha256sum -c damson-bundle-VERSION.tar.gz.sha256
   $ tar -xzf damson-bundle-VERSION.tar.gz
   $ cd damson-bundle-VERSION && ./verify.sh

Install
-------

- **VMs:** follow :doc:`systemd`; the binary and files come from ``bin/`` and
  ``systemd/``.
- **Kubernetes:** load the images into your private registry, then install the
  chart (see :doc:`helm`):

  .. code-block:: console

     $ ./load-images.sh registry.example.com/damson
     $ helm install damson chart/damson-*.tgz -f values.yaml

  ``load-images.sh`` verifies the bundle, loads each image into docker or
  podman, and with a prefix tags and pushes it. Without a prefix it only loads.
  Set ``image.repository`` and ``agents.image.repository`` in the values to the
  registry.

Air-gapped notes
----------------

- No runtime internet access. Docker Hub and other public registries are not
  contacted; the images come from your registry.
- The Ansible plugin needs ``ansible`` on the agent host. The images contain
  the tools pinned in the Dockerfile.

Limits
------

There is no signature yet: image and bundle signing (cosign) is an open
decision (ARCHITECTURE section 22, item 9). The checksums detect corruption.
They detect tampering only if the ``.sha256`` file reaches you by a different
trusted channel than the archive.

CI
--

The ``Bundle`` workflow (:repo:`.github/workflows/bundle.yml`) runs on pull
requests that touch ``deploy/bundle`` or ``deploy/systemd`` and on manual
dispatch, where ``with_images`` adds the images. It builds the bundle, checks
the archive checksum, unpacks it, runs ``verify.sh`` and the binary's
``version``, and checks that the chart and the units are present. The
``systemd`` job of the main CI runs ``make systemd-check``.
