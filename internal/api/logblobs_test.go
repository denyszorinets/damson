package api_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/logblob"
	"github.com/denyszorinets/damson/internal/logblob/s3blob"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/testutil/fakes3"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// TestExecutionLogsAcrossBackends reads one stream whose chunks live in
// different backends: db first, then s3, then a chunk of a backend that is no
// longer configured, which is a gap.
func TestExecutionLogsAcrossBackends(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		u, err := url.Parse(fakes3.Start(t, "logs"))
		require.NoError(t, err)
		s3, err := s3blob.New(s3blob.Options{Endpoint: u.Host, Bucket: "logs", PathStyle: true, AccessKeyID: "a", SecretKey: "b"})
		require.NoError(t, err)
		set, err := logblob.NewSet(logblob.BackendS3, map[string]logblob.Blob{logblob.BackendDB: logblob.DB{S: st}, logblob.BackendS3: s3})
		require.NoError(t, err)

		reg, err := pluginreg.New([]pluginsdk.Manifest{apiManifest("ssh", "1.0.0")}, nil)
		require.NoError(t, err)
		srv := api.NewServer(api.Config{
			Addr: "127.0.0.1:0", Store: st, Data: st, Plugins: reg, UI: ui(), LogBlobs: set,
			Now:    func() time.Time { return t0 },
			Stream: api.StreamConfig{PollInterval: 10 * time.Millisecond, Heartbeat: 20 * time.Millisecond},
			Logger: slog.New(slog.DiscardHandler),
		})
		f := &jrFixture{st: st, h: srv.Handler(), logs: &syncBuffer{}}
		ts := httptest.NewServer(f.h)
		defer ts.Close()
		run, e := f.startedRun(t)

		put := func(backend string, from, to int64, line string) {
			var sb strings.Builder
			for i := from; i <= to; i++ {
				fmt.Fprintf(&sb, "{\"t\":%d,\"s\":\"out\",\"l\":%q}\n", t0.UnixNano(), fmt.Sprintf("%s-%d", line, i))
			}
			key := fmt.Sprintf("logs/%s/%s/%d-%d.ndjson", e.ID, e.AttemptID, from, to)
			ctx := context.Background()
			switch backend {
			case logblob.BackendDB, logblob.BackendS3:
				b := map[string]logblob.Blob{logblob.BackendDB: logblob.DB{S: st}, logblob.BackendS3: s3}[backend]
				require.NoError(t, b.Put(ctx, key, []byte(sb.String())))
			}
			_, err := st.InsertLogChunk(ctx, e.AttemptID, store.NewLogChunk{
				ExecutionID: e.ID, SeqFrom: from, SeqTo: to, Backend: backend, ObjectKey: key,
				Bytes: int64(sb.Len()), TsFirst: t0, TsLast: t0,
			}, t0)
			require.NoError(t, err)
		}
		put(logblob.BackendDB, 1, 2, "d")
		put(logblob.BackendS3, 3, 4, "s")
		put("gcs", 5, 6, "x")
		f.finish(t, run, e, store.ExecSucceeded)

		s := openSSE(t, ts, "/api/v1/executions/"+e.ID+"/logs", nil)
		evs := s.until("end")
		assert.Equal(t, []string{"log:1", "log:2", "log:3", "log:4", "gap:6", "end:"}, names(evs))
		assert.Contains(t, evs[2].Data, "s-3")
		assert.Contains(t, evs[0].Data, "d-1")
	})
}
