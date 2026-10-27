ADR-0023: Project name and namespace
================================================================================

:Status: Proposed
:Date: 2026-10-27

Context
-------

The working name is **Damson** (binary ``damson``, spec ``apiVersion:
damson.dev/v1``, env prefix ``DAMSON_``, secret field marker
``x-damson-secret``). Final checks are pending: GitHub org, Go module path,
Docker Hub and GHCR names, the ``damson.dev`` domain, and a trademark search.
The name is baked into the job spec ``apiVersion``, protocol headers
(``X-Damson-Agent-Id``), metrics names and config keys, so renaming after M2
is costly.

Decision
--------

Not decided yet. This ADR records the question and the options so they can be
evaluated; it changes nothing until it is accepted. Target: before M0 closes.

Alternatives Considered
-----------------------

Keep Damson
~~~~~~~~~~~

Advantages:

- All design documents already use it.

Disadvantages:

- Unverified availability of the namespace.

Rename before M0 closes
~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Cheap now: no code yet.

Disadvantages:

- Design documents need a mechanical rename.

Decision Criteria
-----------------

- Availability of org, module path, registry names, domain.
- Trademark risk.
- Cost of renaming later.

Rationale
---------

Pending the decision.

Consequences
------------

Positive
~~~~~~~~

- To be recorded with the decision.

Negative
~~~~~~~~

- To be recorded with the decision.

Risks
~~~~~

- Deciding late may force changes to already-shipped behaviour; see Context.

Follow-up
~~~~~~~~~

- Enable linkcheck for repository links once the repository is public.
- Fix the Go module path in ``go.mod`` (M0).

Validation
----------

Record the results of each namespace check here.

- **GitHub repository** (2026-10-27): ``github.com/denyszorinets/damson``, set
  as ``repo_url`` in ``docs/conf.py``. Not publicly reachable yet, so
  linkcheck still skips repository links.
- Go module path, Docker Hub/GHCR names, ``damson.dev`` domain, trademark:
  pending.

References
----------

- :doc:`/architecture/design` header and §22 item 2
