// Package dispatch moves executions between the database and agent sockets
// (ARCHITECTURE sections 7.3 and 8.3): the dispatch loop that claims queued
// executions for locally connected agents and sends step.execute, the
// step.complete handler, lease renewal from heartbeats and the reaper.
//
// It keeps no durable state. Every transition is a compare-and-swap in the
// store, fenced by attempt_id; the in-memory slot counts are a cache that the
// agent's own "no free slot" rejection corrects.
package dispatch
