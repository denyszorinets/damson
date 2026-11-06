ADR-0051: Non-secret S3 connection fields
=========================================

:Status: Accepted
:Date: 2026-11-06

Refines :doc:`0047-agent-side-masking-only`.

Context
-------

The agent masks every field value of every secret in the bundle. An S3
connection secret holds ``endpoint``, ``region`` and ``path_style`` next to the
credentials. These are not secret, but they are short and common words. A
region such as ``eu-west-1`` was replaced in logs and outputs, so an object key
like ``backups/eu-west-1/x`` showed up as ``backups/***/x`` in the step log and
in the ``destination_key`` output (:issue:`162`).

Decision
--------

- The agent masker exempts exactly ``endpoint``, ``region`` and ``path_style``
  of a secret that has non-empty ``access_key`` and ``secret_key``.
- A value is exempt only when it has the strict shape; a value that fails a
  guard is masked like any other:

  - ``endpoint``: ``host[:port]`` or ``[ipv6][:port]``, an optional lowercase
    ``http://`` or ``https://`` prefix and one trailing slash, at most 255
    bytes, no userinfo, path, query, fragment or whitespace.
  - ``region``: ``^[a-z0-9_-]+$``, at most 32 bytes.
  - ``path_style``: a Go ``strconv.ParseBool`` value.

- Needles stay a union: a real secret whose value equals an exempt value is
  still masked.
- ``access_key`` and ``session_token`` stay masked.
- The ``transfer`` plugin now rejects an ``endpoint`` with userinfo, a query or
  a fragment as a validation error, so credentials cannot be smuggled into the
  field.
- No protocol, schema, SDK or controller change. The plugin still receives all
  fields.
- ``plugintest`` ``LeakedSecrets`` stays strict: every bundle field counts as a
  secret. It is deliberately stricter than the agent.

Residual risk
~~~~~~~~~~~~~

Secret material put in a field named ``region``, ``endpoint`` or ``path_style``
of an S3-shaped secret, and matching the shape, is not masked. Do not do that.

Alternatives Considered
-----------------------

- Manifest-declared non-secret fields: needs an SDK bump and a mapping from
  secret refs to input fields.
- A per-field marker in the store and bundle: a schema and protocol change. It
  is the right long-term home once a typed ``s3`` secret exists; this allowlist
  would then move there.
- A length threshold: would unmask 8-character passwords.

Consequences
------------

- Keys and outputs that contain the region or endpoint host stay readable.
- The allowlist is a fixed list in the agent, tied to the S3 secret shape.
