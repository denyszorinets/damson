//go:build integration && unix

package openbao_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/denyszorinets/damson/internal/secrets/openbao"
	"github.com/denyszorinets/damson/internal/secrets/secretstest"
)

const (
	baoImage = "openbao/openbao:2.6.5"
	baoPort  = "8200/tcp"
	baoRoot  = "root-token-for-tests"
)

// TestRealOpenBao runs the backend contract against a real OpenBao in dev mode
// (KV v2 mounted at secret/), which keeps the fake server honest.
func TestRealOpenBao(t *testing.T) {
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        baoImage,
			Cmd:          []string{"server", "-dev", "-dev-root-token-id=" + baoRoot, "-dev-listen-address=0.0.0.0:8200"},
			ExposedPorts: []string{baoPort},
			WaitingFor:   wait.ForHTTP("/v1/sys/health").WithPort(baoPort).WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("cannot start the openbao container (is Docker available?): %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := ctr.MappedPort(ctx, baoPort)
	if err != nil {
		t.Fatal(err)
	}
	addr := "http://" + host + ":" + port.Port()
	n := 0
	secretstest.Run(t, func(t *testing.T) secretstest.Fixture {
		n++ // a fresh prefix per case keeps the cases apart
		st := newStore(t)
		c, err := openbao.New(openbao.Config{
			Address: addr, AllowPlainHTTP: true, Prefix: "damson/case" + string(rune('a'+n)),
			Auth: openbao.Auth{Method: openbao.AuthToken, Token: baoRoot},
		})
		if err != nil {
			t.Fatal(err)
		}
		b := openbao.NewBackend(c, st, time.Now)
		if err := b.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = b.Stop(ctx) })
		return secretstest.Fixture{Backend: b, Audit: st}
	})
}
