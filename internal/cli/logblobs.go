package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/logblob"
	"github.com/denyszorinets/damson/internal/logblob/s3blob"
	"github.com/denyszorinets/damson/internal/store"
)

// buildLogBlobs makes the log object backends (ADR-0067). The db backend is
// always there. s3 is added whenever a bucket is configured, so chunks written
// there stay readable after logs.store goes back to db. With logs.store = s3
// the bucket is probed at start and a failure refuses to start.
func buildLogBlobs(ctx context.Context, cfg config.Config, st store.LogStore, log *slog.Logger) (*logblob.Set, error) {
	blobs := map[string]logblob.Blob{logblob.BackendDB: logblob.DB{S: st}}
	if cfg.S3Configured() {
		s, err := newS3Blob(cfg)
		if err != nil {
			return nil, err
		}
		blobs[logblob.BackendS3] = s
		if cfg.Logs.Store == config.LogStoreS3 {
			if err := s.Probe(ctx); err != nil {
				return nil, err
			}
		} else {
			log.Warn("logs.s3 is configured but logs.store is db: s3 chunks stay readable, new chunks go to the database")
		}
		if _, secure := cfg.Logs.S3.HostSecure(); !secure {
			log.Warn("the s3 log endpoint uses plain http; log data and credentials are not encrypted in transit")
		}
	}
	set, err := logblob.NewSet(cfg.Logs.Store, blobs)
	if err != nil {
		return nil, fmt.Errorf("building the log store: %w", err)
	}
	return set, nil
}

func newS3Blob(cfg config.Config) (*s3blob.Store, error) {
	s := cfg.Logs.S3
	secret, err := cfg.ResolveLogsS3Secret()
	if err != nil {
		return nil, err
	}
	var ca []byte
	if s.TLSCAFile != "" {
		if ca, err = os.ReadFile(s.TLSCAFile); err != nil {
			return nil, fmt.Errorf("logs.s3.tls_ca_file: %w", err)
		}
	}
	host, secure := s.HostSecure()
	return s3blob.New(s3blob.Options{
		Endpoint: host, Secure: secure, Region: s.Region, Bucket: s.Bucket, Prefix: s.NormalizedPrefix(),
		PathStyle: s.PathStyle, CAPEM: ca, AccessKeyID: s.AccessKeyID, SecretKey: secret, SessionToken: s.SessionToken,
		SSE: s.SSE, SSEKMSKeyID: s.SSEKMSKeyID,
	})
}
