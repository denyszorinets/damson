package storefactory_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/store/storefactory"
)

func TestOpen(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.Database
		wantErr bool
	}{
		{"sqlite", config.Database{Driver: config.DriverSQLite, DSN: filepath.Join(t.TempDir(), "d.db")}, false},
		{"unknown driver", config.Database{Driver: "mysql", DSN: "x"}, true},
		{"sqlite empty dsn", config.Database{Driver: config.DriverSQLite}, true},
		{"postgres bad dsn", config.Database{Driver: config.DriverPostgres, DSN: "://bad"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := storefactory.Open(context.Background(), tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Open err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil {
				if perr := s.Ping(context.Background()); perr != nil {
					t.Errorf("Ping: %v", perr)
				}
				_ = s.Close()
			}
		})
	}
}
