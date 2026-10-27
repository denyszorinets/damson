ADR-0015: Secret references, envelope encryption and just-in-time delivery
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Steps need credentials (SSH keys, passwords, S3 keys, registry and kubeconfig
credentials). Specs, resolved inputs, logs and outputs are persisted and shown
in the UI. The only required infrastructure is Postgres, but some organisations
mandate an external vault.

Decision
--------

- Specs and stored data contain only **references**:
  ``secret://<project-path>/<name>[#<key>]``, allowed in plugin fields marked
  ``x-damson-secret: true``. Secrets are never CEL values. A job may reference
  secrets of its project or any ancestor; inventory credentials live under
  ``secret://_inventory/``.
- ``SecretStore`` backends, configured globally in v1:

  - ``db`` (default): AES-256-GCM envelope encryption; a random DEK per secret
    version wrapped by a KEK from ``DAMSON_KEK`` or a file; ``kek_id`` per row;
    ``damson admin rotate-kek``.
  - ``openbao`` (Vault-API compatible, KV v2), optionally read-only.

- **Delivery**: the controller resolves refs at dispatch into a secret bundle
  sent inside ``step.execute`` over TLS. It is never persisted by the
  controller and never written to disk by the agent, except short-lived 0600
  credential files on tmpfs where tools require them.
- **Masking**: the agent masks all bundle values (including base64 and URL
  encodings and lines of multi-line values) in logs and outputs; the controller
  re-masks on ingest using hashes kept for the run.
- Any new code path touching a bundle needs a masking test (hard rule 5).

Alternatives Considered
-----------------------

External vault required
~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Breaks "Postgres only"; heavy for small installs.

Secrets as CEL values or environment variables
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Values flow into interpolated, persisted inputs; masking becomes guesswork.

Agents fetch secrets themselves
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Every agent needs vault credentials and network access; agents are no longer
  stateless and simple.

Decision Criteria
-----------------

Confidentiality, required infrastructure, auditability, agent simplicity.

Rationale
---------

References keep values out of every persisted artifact; just-in-time bundles
limit exposure to the dispatch path and agent memory.

Consequences
------------

Positive
~~~~~~~~

- No secret values in the database (except encrypted), logs, API or UI.

Negative
~~~~~~~~

- Losing the KEK means losing all ``db`` secrets; docs must say so loudly.

Risks
~~~~~

- Masking misses transformed values (for example, a script that reverses a
  string). Masking is defence in depth, not a guarantee.

Follow-up
~~~~~~~~~

- M4: ``db`` backend, delivery, masking. M9: OpenBao.
- :doc:`0027-per-project-secret-backends`.

Validation
----------

``plugintest`` masking contract; masking tests for every bundle code path;
security review before v1.

References
----------

- :doc:`/architecture/design` §9, §18
- :doc:`/architecture/security`
