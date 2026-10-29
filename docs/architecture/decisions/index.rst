Architecture decisions
======================

Architecture Decision Records (ADRs) capture decisions that shape Damson's
structure, technology, conventions or operations, with the alternatives and
trade-offs considered. The format and process are described in
:doc:`/development/documentation`.

ADRs 0002–0021 were recorded on 2026-10-27 from the v1 design in
:doc:`/architecture/design`; they document decisions already made there.
Proposed ADRs (0022 onwards) track the open questions from ARCHITECTURE §22 and
change nothing until accepted.

.. list-table::
   :header-rows: 1
   :widths: 8 72 20

   * - No.
     - Decision
     - Status
   * - 0001
     - :doc:`0001-sphinx-and-restructuredtext-for-project-documentation`
     - Accepted
   * - 0002
     - :doc:`0002-single-go-binary-with-controller-and-agent-roles`
     - Accepted
   * - 0003
     - :doc:`0003-postgresql-as-the-only-required-infrastructure`
     - Accepted
   * - 0004
     - :doc:`0004-database-as-source-of-truth-without-consensus`
     - Accepted
   * - 0005
     - :doc:`0005-no-message-broker`
     - Accepted
   * - 0006
     - :doc:`0006-agent-initiated-websocket-with-json-rpc-over-messagepack`
     - Accepted
   * - 0007
     - :doc:`0007-stateless-worker-agents-reaching-targets-remotely`
     - Accepted
   * - 0008
     - :doc:`0008-fencing-tokens-and-execution-leases`
     - Accepted
   * - 0009
     - :doc:`0009-pure-deterministic-engine`
     - Accepted
   * - 0010
     - :doc:`0010-river-for-background-work`
     - Accepted
   * - 0011
     - :doc:`0011-sqlc-and-goose-for-dual-dialect-sql`
     - Accepted
   * - 0012
     - :doc:`0012-cel-as-the-single-expression-language`
     - Accepted
   * - 0013
     - :doc:`0013-json-job-specs-as-versioned-data`
     - Accepted
   * - 0014
     - :doc:`0014-compiled-in-plugins-with-a-public-sdk`
     - Accepted
   * - 0015
     - :doc:`0015-secret-references-and-envelope-encryption`
     - Accepted
   * - 0016
     - :doc:`0016-pluggable-log-store-with-database-default`
     - Accepted
   * - 0017
     - :doc:`0017-oidc-authentication-and-cel-authorization-on-a-project-tree`
     - Accepted
   * - 0018
     - :doc:`0018-huma-http-api-with-generated-openapi-client`
     - Accepted
   * - 0019
     - :doc:`0019-embedded-sveltekit-spa-with-schema-driven-forms`
     - Accepted
   * - 0020
     - :doc:`0020-server-built-job-graph-with-in-house-layout`
     - Accepted
   * - 0021
     - :doc:`0021-dependency-license-policy`
     - Accepted
   * - 0022
     - :doc:`0022-gpl-3-0-or-agpl-3-0`
     - Proposed
   * - 0023
     - :doc:`0023-project-name-and-namespace`
     - Proposed
   * - 0024
     - :doc:`0024-yaml-as-an-input-format`
     - Proposed
   * - 0025
     - :doc:`0025-kerberos-for-winrm`
     - Proposed
   * - 0026
     - :doc:`0026-parallel-groups`
     - Proposed
   * - 0027
     - :doc:`0027-per-project-secret-backends`
     - Proposed
   * - 0028
     - :doc:`0028-directory-and-glob-transfers`
     - Proposed
   * - 0029
     - :doc:`0029-multi-arch-image-builds-and-image-signing`
     - Proposed
   * - 0030
     - :doc:`0030-helm-diff-preview`
     - Proposed
   * - 0031
     - :doc:`0031-manual-approval-steps-and-notifications`
     - Proposed
   * - 0032
     - :doc:`0032-runtime-loaded-plugins`
     - Proposed
   * - 0033
     - :doc:`0033-blueoak-licensed-dev-only-tooling-dependencies`
     - Accepted
   * - 0034
     - :doc:`0034-dev-only-inline-nodes-and-secrets`
     - Accepted

.. toctree::
   :hidden:

   0001-sphinx-and-restructuredtext-for-project-documentation
   0002-single-go-binary-with-controller-and-agent-roles
   0003-postgresql-as-the-only-required-infrastructure
   0004-database-as-source-of-truth-without-consensus
   0005-no-message-broker
   0006-agent-initiated-websocket-with-json-rpc-over-messagepack
   0007-stateless-worker-agents-reaching-targets-remotely
   0008-fencing-tokens-and-execution-leases
   0009-pure-deterministic-engine
   0010-river-for-background-work
   0011-sqlc-and-goose-for-dual-dialect-sql
   0012-cel-as-the-single-expression-language
   0013-json-job-specs-as-versioned-data
   0014-compiled-in-plugins-with-a-public-sdk
   0015-secret-references-and-envelope-encryption
   0016-pluggable-log-store-with-database-default
   0017-oidc-authentication-and-cel-authorization-on-a-project-tree
   0018-huma-http-api-with-generated-openapi-client
   0019-embedded-sveltekit-spa-with-schema-driven-forms
   0020-server-built-job-graph-with-in-house-layout
   0021-dependency-license-policy
   0022-gpl-3-0-or-agpl-3-0
   0023-project-name-and-namespace
   0024-yaml-as-an-input-format
   0025-kerberos-for-winrm
   0026-parallel-groups
   0027-per-project-secret-backends
   0028-directory-and-glob-transfers
   0029-multi-arch-image-builds-and-image-signing
   0030-helm-diff-preview
   0031-manual-approval-steps-and-notifications
   0032-runtime-loaded-plugins
   0033-blueoak-licensed-dev-only-tooling-dependencies
   0034-dev-only-inline-nodes-and-secrets
