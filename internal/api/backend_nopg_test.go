//go:build !integration

package api_test

// postgresBackends is empty without the integration tag; Postgres runs under
// `go test -tags integration` (make test-pg).
func postgresBackends() []backend { return nil }

// stopPostgres is a no-op without the integration tag.
func stopPostgres() {}
