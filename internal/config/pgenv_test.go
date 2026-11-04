package config

import (
	"strings"
	"testing"
)

// The environment the cmd/damson Postgres tests build must load and serve.
func TestPostgresTestEnvLoads(t *testing.T) {
	env := []string{"DAMSON_DATABASE__DRIVER=postgres", "DAMSON_DATABASE__DSN=postgres://u@h/db",
		"DAMSON_CONTROLLER__CLUSTER_SECRET=" + strings.Repeat("t", 40),
		"DAMSON_CONTROLLER__INTERNAL_URL=http://127.0.0.1:1", "DAMSON_CONTROLLER__INTERNAL_LISTEN=127.0.0.1:0"}
	c, err := Load(RoleController, "", env)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateServing(RoleController); err != nil {
		t.Fatal(err)
	}
}
