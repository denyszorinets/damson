package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/store"
)

// AgentStatus is derived from the agent row: an agent that has an owning
// controller is connected.
type AgentStatus string

// Agent statuses.
const (
	AgentConnected    AgentStatus = "connected"
	AgentDisconnected AgentStatus = "disconnected"
)

// Agent is the API view of an agent.
type Agent struct {
	ID             string            `json:"id"`
	PoolID         string            `json:"pool_id"`
	Status         AgentStatus       `json:"status" enum:"connected,disconnected"`
	Version        string            `json:"version"`
	Protocol       int               `json:"protocol"`
	Labels         map[string]string `json:"labels"`
	Capabilities   map[string]string `json:"capabilities" doc:"Plugin name to version."`
	Capacity       int               `json:"capacity"`
	Telemetry      map[string]any    `json:"telemetry,omitempty" doc:"Last telemetry snapshot reported by the agent."`
	ConnectedAt    time.Time         `json:"connected_at"`
	LastSeen       time.Time         `json:"last_seen"`
	DisconnectedAt *time.Time        `json:"disconnected_at,omitempty"`
}

func toAgentView(a store.Agent) Agent {
	v := Agent{
		ID: a.ID, PoolID: a.PoolID, Status: AgentDisconnected, Version: a.Version, Protocol: a.Protocol,
		Labels: a.Labels, Capabilities: a.Capabilities, Capacity: a.Capacity,
		ConnectedAt: a.ConnectedAt, LastSeen: a.LastSeen, DisconnectedAt: a.DisconnectedAt,
	}
	if a.ControllerID != "" {
		v.Status = AgentConnected
	}
	if v.Labels == nil {
		v.Labels = map[string]string{}
	}
	if v.Capabilities == nil {
		v.Capabilities = map[string]string{}
	}
	if len(a.Telemetry) > 0 {
		// A snapshot that is not a JSON object is dropped, not an error.
		var t map[string]any
		if json.Unmarshal(a.Telemetry, &t) == nil {
			v.Telemetry = t
		}
	}
	return v
}

type listInput struct {
	Cursor string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor."`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
}

type listAgentsOutput struct {
	Body struct {
		Items      []Agent `json:"items"`
		NextCursor string  `json:"next_cursor,omitempty" doc:"Empty on the last page."`
	}
}

type getAgentInput struct {
	ID string `path:"id" maxLength:"128"`
}

type getAgentOutput struct{ Body Agent }

func registerAgents(api huma.API, ds DataStore) {
	huma.Register(api, huma.Operation{
		OperationID: "list-agents", Method: http.MethodGet, Path: "/agents",
		Summary: "List agents", Tags: []string{"agents"},
	}, func(ctx context.Context, in *listInput) (*listAgentsOutput, error) {
		if ds == nil {
			return nil, unavailable(ctx)
		}
		page, err := ds.ListAgents(ctx, in.Cursor, in.Limit)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		out := &listAgentsOutput{}
		out.Body.Items = make([]Agent, 0, len(page.Agents))
		for _, a := range page.Agents {
			out.Body.Items = append(out.Body.Items, toAgentView(a))
		}
		out.Body.NextCursor = page.NextCursor
		return out, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "get-agent", Method: http.MethodGet, Path: "/agents/{id}",
		Summary: "Get an agent with its last telemetry", Tags: []string{"agents"},
		Errors: []int{http.StatusNotFound},
	}, func(ctx context.Context, in *getAgentInput) (*getAgentOutput, error) {
		if ds == nil {
			return nil, unavailable(ctx)
		}
		a, err := ds.GetAgent(ctx, in.ID)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		return &getAgentOutput{Body: toAgentView(a)}, nil
	})
}
