ADR-0021: Dependency license policy
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

Damson is licensed GPL-3.0-or-later and distributed as a single binary that
statically links all Go dependencies and embeds the frontend bundle. Every
dependency's license must allow that distribution.

Decision
--------

- Allowed without further review: **MIT, BSD (2- and 3-clause), Apache-2.0,
  MPL-2.0, ISC**. Apache-2.0 is compatible with GPLv3 (not GPLv2).
- Anything else (EPL, LGPL, CDDL, SSPL, BUSL, unlicensed code, …) needs an ADR.
- Each new dependency's license and the reason for adding it are noted in the
  commit message or PR (hard rule 9).
- Documentation tooling under ``docs/`` (Sphinx and extensions) is not
  distributed with the binary but follows the same list.

Alternatives Considered
-----------------------

Case-by-case review only
~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Inconsistent decisions; slow reviews.

Permissive-only (exclude MPL-2.0)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Would exclude River and OpenBao's API client without a real benefit.

Decision Criteria
-----------------

Legal compatibility with GPL-3.0 distribution; review cost.

Rationale
---------

A short allow-list covers the whole planned stack (§2) and makes the common case
a non-event.

Consequences
------------

Positive
~~~~~~~~

- Clear rule for contributors and reviewers.

Negative
~~~~~~~~

- Some useful libraries (ELK.js, EPL-2.0) are excluded.

Risks
~~~~~

- Transitive dependencies with other licenses. Mitigation: automated license
  scan in CI (follow-up).

Follow-up
~~~~~~~~~

- Add a license scanner to CI (M0). Revisit if the project license changes
  (:doc:`0022-gpl-3-0-or-agpl-3-0`).

Validation
----------

CI license report on every dependency change.

References
----------

- :doc:`/architecture/design` §2
