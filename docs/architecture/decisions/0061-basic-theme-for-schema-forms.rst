ADR-0061: Basic theme for schema-driven forms
=============================================

:Status: Accepted
:Date: 2026-11-07

Issue :issue:`197`. Amends ADR-0019 on one point: the theme of ``@sjsf/form``.

Context
-------

ADR-0019 chose ``@sjsf/form`` for forms generated from JSON Schema, with its
shadcn theme. The shadcn theme needs a large set of shadcn-svelte components
(checkbox, switch, radio, calendar and more) that the UI does not otherwise
use, and the first forms (the run dialog from ``spec.options``) need only text,
number, boolean and enum inputs.

Decision
--------

``SchemaForm.svelte`` wraps ``@sjsf/form`` with ``@sjsf/basic-theme`` and the
Ajv validator (``@sjsf/ajv8-validator``), and enables enum selects. It remains
the only place a schema becomes a form. The shadcn theme can replace the basic
one later without changing any caller, because callers see only
``SchemaForm``.

Dependencies and licenses: ``@sjsf/form`` (MIT AND Apache-2.0),
``@sjsf/ajv8-validator`` (MIT), ``@sjsf/basic-theme`` (MIT), and the peers
``json-schema-merge-allof`` (MIT), ``json-schema-typed``,
``@standard-schema/spec`` and ``esm-env``. All are allowed by CLAUDE.md rule 9.

Consequences
------------

- Forms look plainer than the rest of the UI until the theme is swapped.
- No change to the rule that plugins get no UI code of their own.
