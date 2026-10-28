// Package pluginreg is the controller-side plugin registry (ARCHITECTURE
// section 6). It is a derived cache, not state: it holds the manifests the
// controller links itself (built-ins) plus the manifests each live agent sent
// in agent.hello. The gateway calls SetAgent on a successful hello and
// RemoveAgent when the owning socket ends, so the registry mirrors the agents
// connected to this controller. Nothing is persisted; a restarted controller
// rebuilds it as agents reconnect.
//
// The registry resolves "plugin" and "plugin@major" to the best manifest and
// the capable connected agents, and validates a step's resolved input against
// the manifest InputSchema. It imports only pkg/pluginsdk, so the gateway, the
// API and the orchestrator can all use it without import cycles; each
// consumer declares the small interface it needs.
package pluginreg
