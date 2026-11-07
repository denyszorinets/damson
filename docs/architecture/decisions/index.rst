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
     - Superseded by 0044
   * - 0035
     - :doc:`0035-timeout-semantics`
     - Accepted
   * - 0036
     - :doc:`0036-retry-semantics`
     - Accepted
   * - 0037
     - :doc:`0037-groups-scoping-and-status`
     - Accepted
   * - 0038
     - :doc:`0038-for-each-lanes`
     - Accepted
   * - 0042
     - :doc:`0042-db-secret-backend-keyring`
     - Accepted
   * - 0043
     - :doc:`0043-inventory-nodes`
     - Accepted
   * - 0044
     - :doc:`0044-dev-nodes-is-a-seeder`
     - Accepted
   * - 0045
     - :doc:`0045-frozen-run-targets`
     - Accepted
   * - 0046
     - :doc:`0046-tofu-host-key-recording`
     - Accepted (amended by 0048)
   * - 0047
     - :doc:`0047-agent-side-masking-only`
     - Accepted
   * - 0048
     - :doc:`0048-plugins-that-target-several-nodes`
     - Accepted
   * - 0049
     - :doc:`0049-winrm-transport-security-and-remote-execution`
     - Accepted
   * - 0050
     - :doc:`0050-custom-binaries-with-damson-build`
     - Accepted
   * - 0051
     - :doc:`0051-non-secret-s3-connection-fields`
     - Accepted
   * - 0052
     - :doc:`0052-route-steps-by-node-pool-at-dispatch`
     - Accepted
   * - 0053
     - :doc:`0053-audit-events`
     - Accepted
   * - 0054
     - :doc:`0054-audit-retention`
     - Accepted
   * - 0055
     - :doc:`0055-inventory-target-mode`
     - Accepted
   * - 0056
     - :doc:`0056-buildah-registry-credentials`
     - Accepted
   * - 0057
     - :doc:`0057-helm-credentials`
     - Accepted
   * - 0058
     - :doc:`0058-schedules-and-the-scheduler-duty`
     - Accepted
   * - 0059
     - :doc:`0059-webhooks`
     - Accepted
   * - 0060
     - :doc:`0060-service-accounts-and-api-tokens`
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
   0035-timeout-semantics
   0036-retry-semantics
   0037-groups-scoping-and-status
   0038-for-each-lanes
   0039-leader-lease
   0040-internal-controller-endpoints
   0041-job-graph
   0042-db-secret-backend-keyring
   0043-inventory-nodes
   0044-dev-nodes-is-a-seeder
   0045-frozen-run-targets
   0046-tofu-host-key-recording
   0047-agent-side-masking-only
   0048-plugins-that-target-several-nodes
   0049-winrm-transport-security-and-remote-execution
   0050-custom-binaries-with-damson-build
   0051-non-secret-s3-connection-fields
   0052-route-steps-by-node-pool-at-dispatch
   0053-audit-events
   0054-audit-retention
   0055-inventory-target-mode
   0056-buildah-registry-credentials
   0057-helm-credentials
   0058-schedules-and-the-scheduler-duty
   0059-webhooks
   0060-service-accounts-and-api-tokens
