package transfer

import (
	"errors"
	"net/http"
	"testing"

	"github.com/minio/minio-go/v7"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

func TestPartSizeFor(t *testing.T) {
	const mib = 1 << 20
	for name, tc := range map[string]struct {
		size int64
		want uint64
	}{
		"small":          {1 << 20, 64 * mib},
		"at the default": {64 * mib * s3MaxParts, 64 * mib},
		"above":          {64*mib*s3MaxParts + 1, 65 * mib},
		"one TiB":        {1 << 40, 105 * mib}, // ceil(1 TiB / 10000) rounded up to a whole MiB
		"five TiB":       {5 << 40, 525 * mib},
	} {
		t.Run(name, func(t *testing.T) {
			got := partSizeFor(tc.size)
			if got != tc.want || int64(got)*s3MaxParts < tc.size {
				t.Fatalf("partSizeFor(%d) = %d; want %d and room for the object", tc.size, got, tc.want)
			}
		})
	}
}

func TestS3ErrorKinds(t *testing.T) {
	resp := func(code string, status int) error { return minio.ErrorResponse{Code: code, StatusCode: status} }
	for name, tc := range map[string]struct {
		err  error
		want pluginsdk.ErrorKind
	}{
		"access denied":    {resp("AccessDenied", 403), pluginsdk.KindAuth},
		"unknown 403":      {resp("RequestTimeTooSkewed", 403), pluginsdk.KindAuth},
		"no bucket":        {resp("NoSuchBucket", 404), pluginsdk.KindValidation},
		"precondition":     {resp("PreconditionFailed", 412), pluginsdk.KindValidation},
		"not implemented":  {resp("NotImplemented", 501), pluginsdk.KindConnection},
		"too large":        {resp("EntityTooLarge", 400), pluginsdk.KindValidation},
		"invalid argument": {resp("InvalidArgument", 400), pluginsdk.KindValidation},
		"slow down":        {resp("SlowDown", http.StatusServiceUnavailable), pluginsdk.KindConnection},
		"server error":     {resp("InternalError", 500), pluginsdk.KindConnection},
		"throttled":        {resp("", http.StatusTooManyRequests), pluginsdk.KindConnection},
		"request timeout":  {resp("", http.StatusRequestTimeout), pluginsdk.KindConnection},
	} {
		t.Run(name, func(t *testing.T) {
			se, ok := s3Error(tc.err, "x").(*pluginsdk.StepError)
			if !ok || se.Kind != tc.want {
				t.Fatalf("s3Error = %v; want kind %s", se, tc.want)
			}
		})
	}
	// A network error stays as it is: the caller maps it to a connection error.
	plain := errors.New("connection reset")
	if got := s3Error(plain, "x"); got != plain {
		t.Fatalf("s3Error(plain) = %v", got)
	}
}
