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
     SHA256SUMS.sig                                   only when built with SIGNING_KEY
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
With ``SIGNING_KEY=/path/to/private-key`` the build also signs (see Verify).
``VERSION`` comes from ``git describe`` unless you set it. The archive uses
sorted names, owner 0, a fixed modification time and fixed modes. The chart
(``helm package``) and the images (``docker save``) are not byte-reproducible, so
two builds may differ in them; the checksums cover whatever you built. The images
are ``damson``, ``damson-agent`` and ``damson-agent-build`` built from
:repo:`deploy/docker/Dockerfile` (see :doc:`images`).

Verify
------

Without a key, check the archive and then every file:

.. code-block:: console

   $ sha256sum -c damson-bundle-VERSION.tar.gz.sha256
   $ tar -xzf damson-bundle-VERSION.tar.gz
   $ cd damson-bundle-VERSION && ./verify.sh

A checksum alone detects corruption only. To authenticate the maintainers, check
the signature with their public key, which must come from a channel you trust
and **not** from the bundle:

.. code-block:: console

   $ ./verify.sh --pubkey release.pub        # or DAMSON_BUNDLE_PUBKEY=release.pub

With a key, ``verify.sh`` checks the checksums, rejects files that
``SHA256SUMS`` does not list, and requires a valid ``SHA256SUMS.sig`` from that
key. Without a key it says "the signature was NOT checked".
``load-images.sh`` honours ``DAMSON_BUNDLE_PUBKEY`` too. The images (with
``WITH_IMAGES=1``) are listed in ``SHA256SUMS`` and so are covered.

To verify the archive itself before unpacking it, check the ``.sha256`` file's
signature (``<archive>.tar.gz.sha256.sig``, next to the archive):

.. code-block:: console

   $ sha256sum -c damson-bundle-VERSION.tar.gz.sha256
   $ printf 'damson-bundle %s\n' "$(cut -d' ' -f1,2 release.pub)" > signers
   $ ssh-keygen -Y verify -f signers -I damson-bundle -n damson-bundle \
       -s damson-bundle-VERSION.tar.gz.sha256.sig < damson-bundle-VERSION.tar.gz.sha256

Signatures are OpenSSH signatures (``ssh-keygen -Y``, namespace
``damson-bundle``), so verifying needs only ``ssh-keygen`` and works without
network access (ADR-0073).

Signing
~~~~~~~

.. code-block:: console

   $ ssh-keygen -t ed25519 -f damson-release -C "damson release"
   $ SIGNING_KEY=./damson-release make bundle

This writes ``SHA256SUMS.sig`` into the bundle (a signature over ``SHA256SUMS``)
and ``damson-bundle-VERSION.tar.gz.sha256.sig`` next to the archive. Key
management is the maintainers' job: keep the private key offline or in a secret
store, and publish the public key with the release notes and on the project
site.

.. note::

   No release key is published yet, and the project has no release pipeline
   that signs automatically. The ``Bundle`` workflow signs with a throwaway key
   only to prove that the mechanism works. Until a key is published, you can
   check only checksums, or sign a bundle you built yourself with your own key.

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
  With the prefix ``registry.example.com/damson`` set ``image.repository`` to
  ``registry.example.com/damson/damson`` and ``agents.image.repository`` to
  ``registry.example.com/damson/damson-agent``.

Air-gapped notes
----------------

- No runtime internet access. Docker Hub and other public registries are not
  contacted; the images come from your registry.
- The Ansible plugin needs ``ansible`` on the agent host. The images contain
  the tools pinned in the Dockerfile.

Limits
------

The signature is only as good as the public key you hold; see the note above
about the missing release key. The checksums alone detect corruption, and
tampering only if the ``.sha256`` file reaches you by a different trusted
channel than the archive. Container image signing (cosign) remains an open
decision (ARCHITECTURE section 22, item 9). The backup helper scripts are not
part of the bundle; take them from :repo:`deploy/backup` in the source of the
same release (:doc:`/operations/backup`).

CI
--

The ``Bundle`` workflow (:repo:`.github/workflows/bundle.yml`) runs on pull
requests that touch ``deploy/bundle`` or ``deploy/systemd`` and on manual
dispatch, where ``with_images`` adds the images. It builds the bundle, checks
the archive checksum, unpacks it, runs ``verify.sh`` and the binary's
``version``, and checks that the chart and the units are present. The
``systemd`` job of the main CI runs ``make systemd-check``.
