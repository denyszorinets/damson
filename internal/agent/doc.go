// Package agent is the agent role's connection manager: it dials the
// controller, says agent.hello, sends heartbeats with telemetry, and
// reconnects with exponential backoff and full jitter (ARCHITECTURE 7).
package agent
