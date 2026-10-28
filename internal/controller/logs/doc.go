// Package logs is the controller side of the agent log path (ARCHITECTURE
// section 10): it ingests log.append batches, keeps a per-execution ring
// buffer for live tail, persists the lines as chunks in the LogStore and
// acknowledges them to the agent with log.ack only after the chunk row is
// committed.
//
// The Ingest component is created with New and started with Start. The
// gateway wires it to an agent socket with Register (or Handler), and the
// completion handler calls Finish once an execution is terminal.
package logs
