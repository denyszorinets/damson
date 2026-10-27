ADR-0002: One Go binary with controller and agent roles
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Damson has two runtime roles: a controller tier (API, UI, orchestration) and
workers that execute steps. Requirements from :doc:`/architecture/design` §1:

- Simple deployment on VMs, Kubernetes, or a mix; air-gap friendly.
- No outbound internet at runtime; all UI assets embedded.
- Scale target of 1,000 agents and about 500 concurrent step executions.
- Built-in plugins wrap SSH, WinRM, SFTP/FTP/S3 clients, which Go supports well
  with pure-Go libraries.

Decision
--------

Damson is written in **Go** (latest stable) and ships as **one static binary**,
``damson``, with subcommands for each role: ``controller``, ``agent``, ``dev``
(both in one process), ``migrate``, ``apply``, ``export``, ``admin``, ``plugin``
and ``build``. The SvelteKit UI is embedded with ``go:embed``. The binary is
built without cgo (SQLite through ``modernc.org/sqlite``).

Agent container images (``damson-agent``, ``damson-agent-build``) are the same
binary plus external tools (``ansible-core``, ``git``, ``buildah``, ``helm``).

Alternatives Considered
-----------------------

Separate controller and agent binaries
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Advantages:

- Smaller agent binary; clearer separation.

Disadvantages:

- Two release artifacts to version and ship; ``damson dev`` would need both.
- Protocol and plugin code is shared anyway.

A JVM or Python implementation (like Rundeck or Ansible-based tools)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Runtime dependencies on target hosts and agents; heavier footprint;
  harder air-gapped installs.

Decision Criteria
-----------------

Operational simplicity, air-gap friendliness, ecosystem for SSH/WinRM/S3,
concurrency model for many sockets, resource use.

Rationale
---------

A single static binary with no runtime dependencies is the simplest thing to
install, upgrade and move between VMs and Kubernetes. Go's goroutines handle
hundreds of WebSockets per controller trivially (§8.6).

Consequences
------------

Positive
~~~~~~~~

- One artifact, one version; ``damson dev`` runs everything in one process.
- No cgo keeps cross-compilation and static linking simple.

Negative
~~~~~~~~

- Agents carry controller code they do not use (larger binary).

Risks
~~~~~

- Runtime-loaded ``.so`` plugins would require cgo and a separate build flavor
  (:doc:`0032-runtime-loaded-plugins`).

Follow-up
~~~~~~~~~

- M0: ``cmd/damson`` skeleton with config (koanf) and ``/healthz``.

Validation
----------

M0 acceptance: the binary builds with ``CGO_ENABLED=0`` and serves the embedded
SPA.

References
----------

- :doc:`/architecture/design` §2, §16, §19
- :doc:`/architecture/components`
