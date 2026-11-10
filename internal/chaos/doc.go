// Package chaos holds the HA chaos tests (ARCHITECTURE section 20): several
// in-process controllers on one Postgres, agents that can be moved between
// them or cut off, and assertions on database state. The tests are behind the
// "chaos" build tag; run them with `make chaos`.
package chaos
