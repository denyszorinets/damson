//go:build integration && unix

package transfer

import (
	"context"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/internal/s3test"
)

// Without overwrite the PUT carries If-None-Match: *, so the server answers 412
// for an object that appeared after the engine's own stat check, and that
// becomes "destination exists" (this skips the precheck by calling Create).
func TestS3CreateRefusesAnObjectThatAppeared(t *testing.T) {
	conn, client := s3test.StartMinIO(t, "cond-bucket")
	ctx := context.Background()
	if _, err := client.PutObject(ctx, "cond-bucket", "obj", strings.NewReader("old"), 3, minio.PutObjectOptions{}); err != nil {
		t.Fatal(err)
	}
	req := pluginsdk.ExecRequest{Secrets: plugintest.FakeBundle{"secret://s3/conn": conn}}
	fsys, err := dialS3(req, &S3Endpoint{Connection: "secret://s3/conn", Bucket: "cond-bucket", Key: "obj"}, &plugintest.FakeEmitter{})
	if err != nil {
		t.Fatal(err)
	}
	w, err := fsys.Create(ctx, "obj", 3, false)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("new"))
	err = w.Close()
	se, ok := err.(*pluginsdk.StepError)
	if !ok || se.Kind != pluginsdk.KindValidation || !strings.Contains(se.Message, "destination exists") {
		t.Fatalf("Close = %v; want a validation error \"destination exists\"", err)
	}
	// With overwrite the same PUT replaces the object.
	w, err = fsys.Create(ctx, "obj", 3, true)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("new"))
	if err := w.Close(); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
}
