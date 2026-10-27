package protocol

// Method names of ARCHITECTURE.md section 7.2.
const (
	MethodAgentHello          = "agent.hello"           // A->C request
	MethodAgentHeartbeat      = "agent.heartbeat"       // A->C notification
	MethodStepExecute         = "step.execute"          // C->A request
	MethodLogAppend           = "log.append"            // A->C notification
	MethodLogAck              = "log.ack"               // C->A notification
	MethodStepProgress        = "step.progress"         // A->C notification
	MethodStepComplete        = "step.complete"         // A->C request
	MethodStepCancel          = "step.cancel"           // C->A request
	MethodAgentDrain          = "agent.drain"           // C->A notification
	MethodAgentReconnect      = "agent.reconnect"       // C->A notification
	MethodNodeHostKeyObserved = "node.hostkey_observed" // A->C notification
)
