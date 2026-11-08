//go:build integration && unix

package s3blob_test

import (
	"context"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/denyszorinets/damson/internal/logblob"
	"github.com/denyszorinets/damson/internal/logblob/blobtest"
	"github.com/denyszorinets/damson/internal/logblob/s3blob"
)

const (
	minioImage = "quay.io/minio/minio:RELEASE.2025-04-22T22-12-26Z"
	minioPort  = "9000/tcp"
	minioUser  = "minio-test-user"
	minioPass  = "minio-test-password-DO-NOT-LEAK"
)

// TestMinIOContract runs the Blob contract against a real S3 server.
func TestMinIOContract(t *testing.T) {
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: minioImage, Cmd: []string{"server", "/data"}, ExposedPorts: []string{minioPort},
			Env:        map[string]string{"MINIO_ROOT_USER": minioUser, "MINIO_ROOT_PASSWORD": minioPass},
			WaitingFor: wait.ForHTTP("/minio/health/ready").WithPort(minioPort).WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("cannot start the minio container (is Docker available?): %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := ctr.MappedPort(ctx, minioPort)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := host + ":" + port.Port()
	admin, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(minioUser, minioPass, "")})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	blobtest.Run(t, func(t *testing.T) logblob.Blob {
		n++
		bucket := "logs" + string(rune('a'+n))
		if err := admin.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			t.Fatal(err)
		}
		s, err := s3blob.New(s3blob.Options{Endpoint: endpoint, Bucket: bucket, Prefix: "p/", PathStyle: true, AccessKeyID: minioUser, SecretKey: minioPass})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Probe(ctx); err != nil {
			t.Fatal(err)
		}
		return s
	})
}
