package postgres

import (
	"strings"
	"testing"
)

func TestPoolConfigIdleInTransactionTimeout(t *testing.T) {
	tests := []struct {
		name, dsn, want string
	}{
		{"default", "postgres://u:p@localhost/db", "30000"},
		{"key=value dsn", "host=localhost dbname=db", "30000"},
		{"dsn overrides", "postgres://u:p@localhost/db?idle_in_transaction_session_timeout=5s", "5s"},
		{"dsn disables", "postgres://u:p@localhost/db?idle_in_transaction_session_timeout=0", "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := poolConfig(tc.dsn)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.ConnConfig.RuntimeParams[idleInTxParam]; got != tc.want {
				t.Fatalf("%s = %q; want %q", idleInTxParam, got, tc.want)
			}
			if cfg.MaxConns < 2 {
				t.Fatalf("MaxConns = %d; want at least 2", cfg.MaxConns)
			}
		})
	}
}

func TestPoolConfigErrorHidesDSN(t *testing.T) {
	_, err := poolConfig("postgres://user:s3cret@host:notaport/db")
	if err == nil {
		t.Fatal("want an error")
	}
	if msg := err.Error(); len(msg) == 0 || strings.Contains(msg, "s3cret") {
		t.Fatalf("error leaks the DSN: %q", msg)
	}
}
