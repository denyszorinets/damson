package logs

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/logblob"
	"github.com/denyszorinets/damson/internal/logblob/s3blob"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/testutil/fakes3"
)

// flakyBlob fails the first n puts.
type flakyBlob struct {
	logblob.Blob
	fail int
}

func (b *flakyBlob) Put(ctx context.Context, k string, d []byte) error {
	if b.fail > 0 {
		b.fail--
		return errors.New("s3 down")
	}
	return b.Blob.Put(ctx, k, d)
}

func TestFlush_ToS3(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		u, err := url.Parse(fakes3.Start(t, "logs"))
		require.NoError(t, err)
		s3, err := s3blob.New(s3blob.Options{Endpoint: u.Host, Bucket: "logs", PathStyle: true, AccessKeyID: "a", SecretKey: "b"})
		require.NoError(t, err)
		flaky := &flakyBlob{Blob: s3, fail: 2}
		set, err := logblob.NewSet(logblob.BackendS3, map[string]logblob.Blob{logblob.BackendDB: logblob.DB{S: st}, logblob.BackendS3: flaky})
		require.NoError(t, err)

		f := newFx(t, st, func(c *Config) { c.Blobs = set })
		f.mustSend(1, 0, "a", "b")
		for range 2 { // the object write fails: no manifest row, no ack, lines kept
			require.Error(t, f.in.Flush(context.Background(), f.exec.ID))
			assert.Empty(t, f.chunks())
			assert.Empty(t, f.acks.all())
		}
		f.flush()
		cs := f.chunks()
		require.Len(t, cs, 1)
		assert.Equal(t, logblob.BackendS3, cs[0].Backend)
		data, err := s3.Get(context.Background(), cs[0].ObjectKey)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"l":"a"`)
		_, err = st.GetLogData(context.Background(), cs[0].ObjectKey)
		assert.Error(t, err, "the object is not in the database")
	})
}
