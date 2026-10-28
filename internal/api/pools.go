package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/store"
)

// Pool is the API view of an agent pool.
type Pool struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

type listPoolsOutput struct {
	Body struct {
		Items      []Pool `json:"items"`
		NextCursor string `json:"next_cursor,omitempty" doc:"Empty on the last page."`
	}
}

// pageOfPools applies keyset pagination by name (the store's order) to the
// full list. Pools are few, so the store has no paged variant.
func pageOfPools(all []store.Pool, cursor string, limit int) ([]Pool, string) {
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	limit = store.ClampLimit(limit)
	items := make([]Pool, 0, limit)
	next := ""
	for _, p := range all {
		if cursor != "" && p.Name <= cursor {
			continue
		}
		if len(items) == limit {
			next = items[len(items)-1].Name
			break
		}
		items = append(items, Pool{ID: p.ID, Name: p.Name, Description: p.Description, CreatedAt: p.CreatedAt})
	}
	return items, next
}

func registerPools(api huma.API, ds DataStore) {
	huma.Register(api, huma.Operation{
		OperationID: "list-pools", Method: http.MethodGet, Path: "/pools",
		Summary: "List agent pools", Tags: []string{"pools"},
	}, func(ctx context.Context, in *listInput) (*listPoolsOutput, error) {
		if ds == nil {
			return nil, unavailable(ctx)
		}
		all, err := ds.ListPools(ctx)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		out := &listPoolsOutput{}
		out.Body.Items, out.Body.NextCursor = pageOfPools(all, in.Cursor, in.Limit)
		return out, nil
	})
}
