package node_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/controller/node"
	"github.com/denyszorinets/damson/internal/pluginreg"
	secretsdb "github.com/denyszorinets/damson/internal/secrets/db"
	"github.com/denyszorinets/damson/internal/store"
)

func kek() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func startWithKEK(t *testing.T, st store.Store, k string) (*node.Node, error) {
	t.Helper()
	reg, err := pluginreg.New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return node.Start(context.Background(), node.Config{Store: st, ControllerID: "c", Plugins: reg, InsecureDev: true, KEK: k})
}

// The KEK startup rules, end to end through node.Start.
func TestStartKEKRules(t *testing.T) {
	ctx := context.Background()
	k, other := kek(), kek()

	st := migratedStore(t)
	n, err := startWithKEK(t, st, "")
	if err != nil {
		t.Fatalf("no KEK, no canary must start: %v", err)
	}
	stopNode(t, n)
	if _, err := st.GetSetting(ctx, secretsdb.CanaryKey); err == nil {
		t.Fatal("a canary was written without a KEK")
	}

	if n, err = startWithKEK(t, st, k); err != nil {
		t.Fatalf("first start with a KEK: %v", err)
	}
	stopNode(t, n)
	if _, err := st.GetSetting(ctx, secretsdb.CanaryKey); err != nil {
		t.Fatalf("canary not stored: %v", err)
	}
	if n, err = startWithKEK(t, st, k); err != nil {
		t.Fatalf("restart: %v", err)
	}
	stopNode(t, n)

	if n, err = startWithKEK(t, st, ""); err == nil {
		stopNode(t, n)
		t.Fatal("started without a KEK although the database holds a canary")
	}
	if n, err = startWithKEK(t, st, other); err == nil {
		stopNode(t, n)
		t.Fatal("started with a KEK that does not match")
	} else if strings.Contains(err.Error(), other) || strings.Contains(err.Error(), k) {
		t.Fatalf("error prints a key: %v", err)
	}
	if n, err = startWithKEK(t, st, "short"); err == nil {
		stopNode(t, n)
		t.Fatal("started with an invalid KEK")
	}
}
