ADR-0073: Bundle signing with OpenSSH signatures and backup scripts
===================================================================

:Status: Accepted
:Date: 2026-11-09
:Amends: ARCHITECTURE section 16 (offline bundle)

Issue :issue:`233`. Related: ADR-0071 and ADR-0072 (the same release train).

Context
-------

The offline bundle had checksums only, which detect corruption but do not
authenticate the maintainers. Air-gapped sites must be able to verify a
signature without network access. Backup was documented as "use your database's
tools" with no tested procedure.

Decision
--------

**Signing.** The bundle uses OpenSSH signatures: ``ssh-keygen -Y sign`` and
``-Y verify`` with the namespace ``damson-bundle`` (ed25519 recommended).
``SIGNING_KEY=/path/to/key`` makes ``deploy/bundle/build.sh`` write
``SHA256SUMS.sig`` into the bundle and ``<archive>.tar.gz.sha256.sig`` next to
the archive. ``verify.sh --pubkey FILE`` (or ``DAMSON_BUNDLE_PUBKEY``, also read
by ``load-images.sh``) verifies the checksums, rejects unlisted files and
requires a valid signature. Without a key it verifies checksums only and says
that the signature was not checked. Images are covered through ``SHA256SUMS``.
The public key must come from a channel the operator trusts, not the bundle.

**Keys.** Maintainers generate the key, keep the private key offline and publish
the public key with the release notes. No release key is published yet and no
release pipeline signs automatically; the CI bundle workflow signs with a
throwaway key only to test the mechanism.

**Backup.** ``deploy/backup/damson-backup.sh`` and ``damson-restore.sh`` wrap
``pg_dump``/``pg_restore`` (or ``sqlite3 .backup``), read the controller's
``DAMSON_DATABASE__*`` variables, and write a folder with a manifest. They are
tested in CI (``make backup-check``) and are not part of the bundle.

Alternatives
------------

- **cosign/sigstore.** Rejected: keyless signing and verification need network
  access (Fulcio, Rekor), which breaks verification in an air-gapped site.
- **minisign or GPG.** Rejected: an extra tool and, for GPG, a keyring to
  manage. ``ssh-keygen`` is already on almost every host.
- **No signing.** Rejected: checksums give no authenticity.
- **A ``damson backup`` command.** Rejected: ``pg_dump`` is the right tool for
  PostgreSQL, and a command in the binary would be a new code path near the
  database and secrets that must be tested and masked for no gain.

Consequences
------------

- Verification needs only ``ssh-keygen`` and works offline.
- Signing protects only once a release key is published and distributed.
- Container image signing (ARCHITECTURE section 22, item 9) stays open.
- Backups are logical dumps: the RPO is the backup age; point-in-time recovery
  still needs WAL archiving. The scripts do not rotate or ship backups.
