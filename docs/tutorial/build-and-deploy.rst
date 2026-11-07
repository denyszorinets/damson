Build, deploy and roll back
===========================

This job builds an image from git with the ``buildah`` plugin, deploys it with
the ``helm`` plugin using the digest the build produced, smoke tests the result
and rolls back if the smoke test fails. The file is
:repo:`examples/jobs/build-and-deploy.json`, and a test keeps it valid: it parses
the job and checks that every plugin step's input, with the expressions
resolved, is accepted by that plugin.

What you need
-------------

* A **build pool** of agents from the ``damson-agent-build`` image
  (:doc:`/reference/images`), kept apart from your other pools because a
  Containerfile and a Helm chart are code that runs on the agent.
* Secrets in the job's project (or an ancestor):

  .. list-table::
     :widths: 30 70
     :header-rows: 1

     * - Secret
       - Content
     * - ``secret://team/git-ro``
       - A ``token`` secret to clone the repository over https.
     * - ``secret://team/registry-push``
       - A ``password`` secret (``username``, ``password``) that may push the image.
     * - ``secret://team/registry-pull``
       - A ``password`` secret that may pull the chart from the OCI registry.
     * - ``secret://team/kube-prod``
       - A ``generic`` secret with the field ``kubeconfig``: a self-contained
         kubeconfig (a token and inline certificate data; no file paths, ``exec``
         or ``auth-provider``, see :doc:`/reference/plugin-helm`).

* An inventory node named ``ops-bastion`` that can reach the application, for the
  smoke test.

The steps
---------

``build``
   ``buildah`` clones ``repo.url`` at ``inputs.ref``, builds, tags the image
   with ``inputs.version`` and pushes it. Its outputs are ``image``, ``digest``,
   ``image_ref``, ``tags`` and ``commit``.

``deploy`` (a group)
   ``upgrade`` runs ``helm`` with ``values.image.digest`` set to
   ``${{ steps.build.outputs.digest }}``: the cluster runs exactly the bits that
   were pushed, not whatever the tag points to later. ``atomic`` (the default)
   makes helm roll a failed upgrade back by itself. ``smoke`` curls the health
   endpoint through ``ops-bastion`` and is retried three times, because the
   service may need a moment after the upgrade.

   ``catch`` runs ``rollback`` when ``upgrade`` or ``smoke`` fails. It covers the
   case ``atomic`` cannot: an upgrade that succeeded but whose smoke test
   failed. ``rethrow: true`` then fails the group (and the run) with the original
   error after the rollback, so the failure is not hidden.

Timeouts
--------

``upgrade`` and ``rollback`` have a step ``timeout`` of ``15m``, above helm's own
``timeout`` of ``10m`` plus the 60 seconds helm is given to roll back after a
cancel. A step timeout shorter than that could kill helm in the middle of an
atomic rollback and leave the release pending
(:doc:`/reference/plugin-helm`).

Run it
------

Create the job in the project ``team/app`` and trigger it with the two options::

   {"inputs": {"ref": "main", "version": "1.4.0"}}

Variations
----------

* Pin the chart with ``chart.git`` instead of ``chart.oci`` to deploy a chart
  from the application repository.
* To deploy several clusters, wrap ``upgrade`` and ``smoke`` in a ``for_each``
  group over ``inputs.clusters``.
* To keep a failed build image out of the registry, set ``push: false`` on a
  first ``buildah`` step that only builds, and push on the next step.
