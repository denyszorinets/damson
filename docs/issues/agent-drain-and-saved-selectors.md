---
title: Agent drain and server-side saved node selectors
type: feature
status: open
source: ui-r14-admin-pages
---

## Context

R14 of the UI overhaul asked for agent drain and saved node selectors. Neither has backend support, and both are decisions the maintainer reserves (protocol and database schema), so R14 built the rest and left these.

## Problem

- Drain: an operator cannot mark an agent as draining so it takes no new work while running attempts finish. There is no API, store field or protocol message for it. Needs a design: where the state lives (agents table), how dispatch honors it, what the agent is told (protocol N-1 compatibility), audit, and the UI control with its role.
- Saved selectors: the nodes page keeps named selectors in the browser (`localStorage`), per browser rather than per user or team. A server table (name, selector, owner or instance scope, version) would share them; it needs a migration on both databases and a decision on ownership.

## Acceptance

- A decision (ADR) for each, then the slice that builds it.
