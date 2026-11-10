ADR-0033: BlueOak-1.0.0 for dev-only tooling dependencies
================================================================================

:Status: Accepted
:Date: 2026-10-27

Context
-------

:doc:`0021-dependency-license-policy` allows MIT, BSD, Apache-2.0, MPL-2.0 and
ISC and requires an ADR for anything else. The first case outside that list is
``minimatch``, a transitive npm dev dependency of the frontend lint tooling
(introduced by PR #20), which is licensed BlueOak-1.0.0. BlueOak-1.0.0 is a
permissive license, but it is not on the list. The package is used only while
developing and building; none of its code is linked into the Go binary or
included in the web bundle.

Decision
--------

- Dependencies licensed **BlueOak-1.0.0** are accepted when they are dev-only
  tooling: used to lint, test, build or document Damson, and never linked into
  the shipped binary or included in the web bundle.
- Shipped dependencies (Go modules linked into the binary and npm packages that
  end up in the embedded web bundle) stay on MIT, BSD, Apache-2.0, MPL-2.0 and
  ISC, as defined in :doc:`0021-dependency-license-policy`.
- Any other license for dev-only tooling still needs its own ADR.
- A dev-only dependency that becomes shipped (for example by being imported
  from application code) must be re-evaluated against the shipped list.

Alternatives Considered
-----------------------

Pin or replace the tooling to avoid BlueOak
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Fights the transitive dependency tree of mainstream lint tools; high
  maintenance for no distribution benefit.

Allow BlueOak-1.0.0 everywhere
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

Disadvantages:

- Widens the shipped list without need; the narrow scope is enough.

Decision Criteria
-----------------

No effect on the license of the distributed binary; minimal growth of the
allow-list; low maintenance.

Rationale
---------

Dev-only tooling is not distributed, so its license does not constrain the
GPL-3.0-or-later distribution that motivated ADR-0021. BlueOak-1.0.0 is
permissive, so the review cost is low.

Consequences
------------

Positive
~~~~~~~~

- Mainstream frontend tooling can be used without per-package exceptions.

Negative
~~~~~~~~

- Two license tiers (shipped and dev-only) must be kept apart.

Risks
~~~~~

- A BlueOak package leaks into the web bundle. Mitigation: the production
  license check below.

Follow-up
~~~~~~~~~

- When the license scanner from ADR-0021 is added to CI, configure it with the
  two tiers.

Validation
----------

Confirm no BlueOak-1.0.0 package is shipped:

- Go: the license report over ``go list -deps`` of the ``damson`` binary lists
  none.
- Web: in ``web/``, run ``npx license-checker --production --summary`` (or
  ``npm ls --omit=dev --all`` and check each license); no BlueOak-1.0.0 entry
  may appear among production dependencies.

References
----------

- :doc:`0021-dependency-license-policy`
- CLAUDE.md hard rule 9
