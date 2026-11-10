//go:build !integration

package cli

// postgresBackends is empty without the integration tag; Postgres runs under
// `go test -tags integration` (make test-pg).
func postgresBackends() []adminBackend { return nil }

// stopPostgres is a no-op without the integration tag.
func stopPostgres() {}
