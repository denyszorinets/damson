package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// PluginCatalog is what the plugin endpoints read. *pluginreg.Registry
// implements it.
type PluginCatalog interface {
	List() []pluginreg.Plugin
	Get(name string) (pluginreg.Plugin, bool)
	// Lookup and ValidateInput serve authoring-time job validation: the
	// highest known manifest for a plugin reference, and a cached check of
	// step input against its schema.
	Lookup(ref string) (pluginsdk.Manifest, error)
	ValidateInput(m pluginsdk.Manifest, input json.RawMessage) error
}

// PluginProvider is an agent that runs a plugin version.
type PluginProvider struct {
	AgentID string `json:"agent_id"`
	Pool    string `json:"pool" doc:"Pool name."`
}

// PluginConflict reports agents whose manifest for the same name and version
// differs from the accepted one. They receive no work until fixed.
type PluginConflict struct {
	Checksum string           `json:"checksum"`
	Agents   []PluginProvider `json:"agents"`
}

// PluginVersion is one version of a plugin with its manifest.
type PluginVersion struct {
	Version      string           `json:"version" doc:"Semantic version."`
	Checksum     string           `json:"checksum" doc:"SHA-256 of the canonical manifest."`
	Title        string           `json:"title"`
	Description  string           `json:"description"`
	InputSchema  map[string]any   `json:"input_schema" doc:"JSON Schema of the resolved step input."`
	OutputSchema map[string]any   `json:"output_schema" doc:"JSON Schema of the step outputs."`
	Idempotent   bool             `json:"idempotent"`
	Requires     []string         `json:"requires" doc:"Agent capabilities needed, such as bin:git."`
	TargetMode   string           `json:"target_mode" enum:"none,single_node,inventory"`
	Builtin      bool             `json:"builtin" doc:"The controller links this exact manifest itself."`
	Pools        []string         `json:"pools" doc:"Pools with at least one connected agent running this version."`
	Agents       []PluginProvider `json:"agents" doc:"Connected agents running this version."`
	Conflicts    []PluginConflict `json:"conflicts" doc:"Agents reporting a different manifest for this version; empty when consistent."`
}

// Plugin is the API view of one plugin name; versions are highest first.
type Plugin struct {
	Name     string          `json:"name"`
	Versions []PluginVersion `json:"versions"`
}

func toPluginView(p pluginreg.Plugin) (Plugin, error) {
	out := Plugin{Name: p.Name, Versions: make([]PluginVersion, 0, len(p.Versions))}
	for _, v := range p.Versions {
		in, err := schemaMap(v.Manifest.InputSchema)
		if err != nil {
			return Plugin{}, fmt.Errorf("plugin %s@%s input schema: %w", p.Name, v.Manifest.Version, err)
		}
		outSch, err := schemaMap(v.Manifest.OutputSchema)
		if err != nil {
			return Plugin{}, fmt.Errorf("plugin %s@%s output schema: %w", p.Name, v.Manifest.Version, err)
		}
		pv := PluginVersion{
			Version: v.Manifest.Version, Checksum: v.Checksum, Title: v.Manifest.Title,
			Description: v.Manifest.Description, InputSchema: in, OutputSchema: outSch,
			Idempotent: v.Manifest.Idempotent, Requires: nonNil(v.Manifest.Requires),
			TargetMode: string(v.Manifest.TargetMode), Builtin: v.Builtin,
			Pools: nonNil(v.Pools()), Agents: providers(v.Agents), Conflicts: []PluginConflict{},
		}
		for _, c := range v.Conflicts {
			pv.Conflicts = append(pv.Conflicts, PluginConflict{Checksum: c.Checksum, Agents: providers(c.Agents)})
		}
		out.Versions = append(out.Versions, pv)
	}
	return out, nil
}

func providers(ps []pluginreg.Provider) []PluginProvider {
	out := make([]PluginProvider, len(ps))
	for i, p := range ps {
		out[i] = PluginProvider{AgentID: p.AgentID, Pool: p.Pool}
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func schemaMap(s any) (map[string]any, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

type listPluginsOutput struct {
	Body struct {
		Items []Plugin `json:"items"`
	}
}

type getPluginInput struct {
	Name string `path:"name" maxLength:"64"`
}

type getPluginOutput struct{ Body Plugin }

// registerPlugins serves the registry. The list is not paged: it is bounded by
// the plugins compiled into the agent binaries.
func registerPlugins(api huma.API, pc PluginCatalog) {
	huma.Register(api, huma.Operation{
		OperationID: "list-plugins", Method: http.MethodGet, Path: "/plugins",
		Summary: "List plugins with manifests, schemas and the agents that provide them", Tags: []string{"plugins"},
	}, func(ctx context.Context, _ *struct{}) (*listPluginsOutput, error) {
		if pc == nil {
			return nil, unavailable(ctx)
		}
		out := &listPluginsOutput{}
		out.Body.Items = []Plugin{}
		for _, p := range pc.List() {
			v, err := toPluginView(p)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			out.Body.Items = append(out.Body.Items, v)
		}
		return out, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "get-plugin", Method: http.MethodGet, Path: "/plugins/{name}",
		Summary: "Get one plugin with all known versions", Tags: []string{"plugins"},
		Errors: []int{http.StatusNotFound},
	}, func(ctx context.Context, in *getPluginInput) (*getPluginOutput, error) {
		if pc == nil {
			return nil, unavailable(ctx)
		}
		p, ok := pc.Get(in.Name)
		if !ok {
			return nil, apiError(ctx, fmt.Errorf("plugin %q: %w", in.Name, store.ErrNotFound))
		}
		v, err := toPluginView(p)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		return &getPluginOutput{Body: v}, nil
	})
}
