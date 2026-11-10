package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
)

// RunTarget is one node of a run's frozen target list: the node as the
// selector saw it when the run started. It carries no credential or host key.
type RunTarget struct {
	Name       string         `json:"name"`
	NodeID     string         `json:"node_id"`
	Pool       string         `json:"pool"`
	OS         string         `json:"os"`
	Transport  string         `json:"transport"`
	Address    string         `json:"address"`
	Tags       []string       `json:"tags"`
	Attributes map[string]any `json:"attributes"`
}

type runTargetsInput struct {
	ID     string `path:"id" maxLength:"64"`
	Cursor string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor."`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
}

type runTargetsOutput struct {
	Body struct {
		Items      []RunTarget `json:"items"`
		NextCursor string      `json:"next_cursor,omitempty" doc:"Empty on the last page."`
	}
}

func registerRunTargets(api huma.API, d deps) {
	huma.Register(api, huma.Operation{
		OperationID: "list-run-targets", Method: http.MethodGet, Path: "/runs/{id}/targets",
		Summary: "List the nodes a run froze as its targets", Tags: []string{"runs"},
		Security: adminSecurity, Middlewares: huma.Middlewares{requireAdmin(api, d)},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusBadRequest, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *runTargetsInput) (*runTargetsOutput, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		cur, err := decodeCursor(ctx, in.Cursor)
		if err != nil {
			return nil, err
		}
		after := 0
		if cur != "" {
			if after, err = strconv.Atoi(cur); err != nil || after < 0 {
				return nil, apiError(ctx, invalid("invalid cursor"))
			}
		}
		if _, err := d.ds.GetRun(ctx, in.ID); err != nil {
			return nil, apiError(ctx, err)
		}
		rows, err := d.ds.ListRunTargetsPage(ctx, in.ID, after, in.Limit+1)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		out := &runTargetsOutput{}
		out.Body.Items = []RunTarget{}
		if len(rows) > in.Limit {
			rows = rows[:in.Limit]
			out.Body.NextCursor = encodeCursor(strconv.Itoa(rows[len(rows)-1].Position))
		}
		for _, r := range rows {
			var n struct {
				Pool, OS, Transport, Address string
				Tags                         []string
				Attributes                   map[string]any
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(r.Node, &m); err == nil {
				_ = json.Unmarshal(m["pool"], &n.Pool)
				_ = json.Unmarshal(m["os"], &n.OS)
				_ = json.Unmarshal(m["transport"], &n.Transport)
				_ = json.Unmarshal(m["address"], &n.Address)
				_ = json.Unmarshal(m["tags"], &n.Tags)
				_ = json.Unmarshal(m["attributes"], &n.Attributes)
			}
			if n.Tags == nil {
				n.Tags = []string{}
			}
			if n.Attributes == nil {
				n.Attributes = map[string]any{}
			}
			out.Body.Items = append(out.Body.Items, RunTarget{Name: r.Name, NodeID: r.NodeID, Pool: n.Pool, OS: n.OS,
				Transport: n.Transport, Address: n.Address, Tags: n.Tags, Attributes: n.Attributes})
		}
		return out, nil
	})
}
