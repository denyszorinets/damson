package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
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

// pageOfPools applies keyset pagination to the full pool list. The cursor is
// the name of the last pool of the previous page, so the list must be in name
// order: store.ListPools documents that order, and it is re-established here
// on a copy so correctness never depends on the backend. Pools are few, so the
// store has no paged variant.
func pageOfPools(pools []store.Pool, cursor string, limit int) ([]Pool, string) {
	all := slices.Clone(pools)
	slices.SortFunc(all, func(a, b store.Pool) int { return strings.Compare(a.Name, b.Name) })
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
		cursor, err := decodeCursor(ctx, in.Cursor)
		if err != nil {
			return nil, err
		}
		all, err := ds.ListPools(ctx)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		out := &listPoolsOutput{}
		out.Body.Items, out.Body.NextCursor = pageOfPools(all, cursor, in.Limit)
		out.Body.NextCursor = encodeCursor(out.Body.NextCursor)
		return out, nil
	})
}
