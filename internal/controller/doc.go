// Package controller holds the controller-side components of the agent link.
//
// Gateway serves the /agent/v1/connect WebSocket endpoint (ARCHITECTURE 7.1):
// it authenticates the pool token before the upgrade, handles agent.hello and
// agent.heartbeat, keeps an in-memory registry of live sockets, batches
// telemetry into the database every 10 seconds, registers the controller
// instance and sends agent.reconnect to every socket on graceful shutdown.
//
// The registry is a cache of sockets only. Ownership of an agent is decided by
// the database (session_seq fencing); nothing durable lives in memory.
package controller
