// Package protocol implements the agent to controller wire protocol of
// ARCHITECTURE.md section 7: JSON-RPC 2.0 objects encoded as MessagePack in
// binary frames of at most 4 MiB, the message types of every method in section
// 7.2, and a bidirectional Peer that correlates requests and responses over a
// small Conn interface. A WebSocket adapter (WSConn) provides the transport.
//
// The package has no knowledge of the store or the engine. Fencing by
// attempt_id (section 7.3) is applied by the receiving component; the message
// types here only carry the attempt_id.
package protocol
