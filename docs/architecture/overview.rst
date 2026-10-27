Overview
========

Damson is a job orchestrator in the spirit of Rundeck and a minimal Jenkins.
A **job** is a JSON spec made of **steps**; each step calls a **plugin** with
parameters. Steps run on stateless **agents** that connect out to a highly
available **controller** tier. PostgreSQL is the only required infrastructure.

System context
--------------

.. mermaid::

   flowchart TB
     users["Users, CI, CMDB sync, webhooks"] -->|HTTPS| lb["Load balancer<br/>(no stickiness)"]
     lb --> c1["controller A<br/>API + UI + WS"]
     lb --> c2["controller N<br/>API + UI + WS"]
     c1 <-->|internal HTTP<br/>log tail, cancel hints| c2
     c1 & c2 --> pg[("PostgreSQL<br/>source of truth")]
     c1 & c2 -.->|optional| s3[("S3 logs")]
     c1 & c2 -.->|optional| bao[("OpenBao")]
     a1["agent pool=dc1"] -->|"wss:// (agent-initiated)"| lb
     a2["agent pool=k8s"] -->|"wss://"| lb
     a1 -->|SSH / WinRM / SFTP / FTP / S3| t1["target nodes<br/>(no Damson software)"]
     a2 -->|Helm, registry| t2["Kubernetes, registries"]

Key properties
--------------

- **Controllers are disposable.** They hold no durable state; any controller
  serves any request and can be killed at any time
  (:doc:`decisions/0004-database-as-source-of-truth-without-consensus`).
- **Agents connect out.** Nothing connects inbound to an agent, so agents work
  behind NAT, firewalls and in Kubernetes
  (:doc:`decisions/0006-agent-initiated-websocket-with-json-rpc-over-messagepack`).
- **Agents are workers, not node-resident.** One pool per network zone reaches
  targets remotely (:doc:`decisions/0007-stateless-worker-agents-reaching-targets-remotely`).
- **Runs survive controller bounces.** Agents keep executing, reconnect
  elsewhere and re-deliver buffered logs and completions; fencing makes replays
  safe (:doc:`decisions/0008-fencing-tokens-and-execution-leases`).
- **No broker, no consensus cluster**
  (:doc:`decisions/0005-no-message-broker`).
- **File data never passes through the controller.**

Quality priorities
------------------

In order: correctness, simplicity, maintainability, reliability, security,
operability, performance, developer productivity, cost. "Simplicity is a
feature": a new required service needs an ADR.

Scale target for v1: 1,000 connected agents and about 500 concurrent step
executions (:doc:`design`, §8.6).

Scope
-----

v1 goals and non-goals are listed in :doc:`design` §1. Notable non-goals:
runtime-loaded plugins, dynamic inventory providers, Git sync of jobs, a
drag-and-drop editor, and parallel branches other than node fan-out.

Where to read next
------------------

.. list-table::
   :header-rows: 1

   * - Topic
     - Page
     - Specification
   * - Packages and boundaries
     - :doc:`components`
     - §19
   * - Domain model, run lifecycle, logs
     - :doc:`data`
     - §4, §8, §10
   * - Secrets, authentication, authorization
     - :doc:`security`
     - §9, §12, §18
   * - Job spec and control flow
     - :doc:`/concepts/jobs-and-runs`
     - §5
   * - Plugins and SDK
     - :doc:`/concepts/agents-and-plugins`
     - §6
