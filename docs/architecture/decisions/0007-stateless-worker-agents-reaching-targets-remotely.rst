ADR-0007: Stateless worker agents that reach targets remotely
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Jobs act on target nodes (Linux and Windows servers, S3 buckets, Kubernetes
clusters). Networks are segmented and sometimes air-gapped. Installing software
on every target is expensive and often forbidden. Agents must be restartable
and autoscalable without losing runs.

Decision
--------

- Agents are **workers, not node-resident daemons**. They reach targets over
  SSH, WinRM, SFTP, SCP, FTP/FTPS, S3, or Kubernetes APIs. Target nodes need no
  Damson software.
- Agents are grouped in **pools**; a pool is placed in each network zone. Step
  dispatch selects a pool from ``runs_on`` or the target node's pool.
- Agents are **stateless**: each ``step.execute`` carries the resolved input,
  node connection info and the secret bundle. Work directories are per execution
  and removed afterwards.
- File data streams between endpoints through the agent's memory and never
  through the controller.
- Agents advertise capacity (``max_concurrent``) and capabilities (plugins,
  detected binaries); the controller never dispatches beyond free slots.

Alternatives Considered
-----------------------

Node-resident agents (one per target)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- No remote-access credentials needed.

Disadvantages:

- Software rollout to every host; Windows and appliance support; much larger
  fleet to manage.

Controller executes steps directly
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Controllers would need reachability into every zone and would carry
  execution load and file traffic.

Decision Criteria
-----------------

Network segmentation, operational cost, failure isolation, scalability.

Rationale
---------

Pools of stateless workers per zone match how operators already place
bastions and jump hosts. Statelessness lets pools autoscale and restart freely.

Consequences
------------

Positive
~~~~~~~~

- Nothing to install on targets; agents are cattle.

Negative
~~~~~~~~

- Agents hold credentials in memory during execution.
- An agent crash loses unacknowledged logs (accepted trade-off).

Risks
~~~~~

- Credential exposure on a compromised agent. Mitigation: just-in-time bundles,
  masking, short-lived 0600 files on tmpfs
  (:doc:`0015-secret-references-and-envelope-encryption`).

Follow-up
~~~~~~~~~

- M1: pools and capacity; M2: dispatch by pool and capabilities.

Validation
----------

Chaos test kills agents mid-run; affected executions become ``lost`` and are
retried when allowed.

References
----------

- :doc:`/architecture/design` §3, §7.3, §16
