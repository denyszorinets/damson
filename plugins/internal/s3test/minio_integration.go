//go:build integration && unix

package s3test

import (
	"context"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// MinIO is a real S3 server: it implements multipart uploads, conditional puts
// (If-None-Match) and versioning, which the in-process fake does not.
const (
	minioImage = "minio/minio:RELEASE.2025-04-22T22-12-26Z"
	minioPort  = "9000/tcp"
	// MinIOUser and MinIOPassword are the root credentials of the container.
	MinIOUser     = "minio-test-user"
	MinIOPassword = "minio-test-password-DO-NOT-LEAK"
)

// StartMinIO runs a MinIO container with the given buckets and returns the
// fields of a connection secret for it and an admin client.
func StartMinIO(t *testing.T, buckets ...string) (map[string]string, *minio.Client) {
	t.Helper()
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        minioImage,
			Cmd:          []string{"server", "/data"},
			ExposedPorts: []string{minioPort},
			Env:          map[string]string{"MINIO_ROOT_USER": MinIOUser, "MINIO_ROOT_PASSWORD": MinIOPassword},
			WaitingFor:   wait.ForHTTP("/minio/health/ready").WithPort(minioPort).WithStartupTimeout(2 * time.Minute),
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
	client, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(MinIOUser, MinIOPassword, "")})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range buckets {
		if err := client.MakeBucket(ctx, b, minio.MakeBucketOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return map[string]string{
		"endpoint": "http://" + endpoint, "region": "us-east-1", "access_key": MinIOUser, "secret_key": MinIOPassword, "path_style": "true",
	}, client
}
