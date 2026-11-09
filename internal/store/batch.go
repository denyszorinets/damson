package store

// MaxBatch is the most values one batch query binds: it keeps the statement
// well below the variable limits of both databases.
const MaxBatch = 500

// Chunks splits s into slices of at most size values; nil for an empty s.
func Chunks[T any](s []T, size int) [][]T {
	var out [][]T
	for len(s) > 0 {
		n := min(size, len(s))
		out = append(out, s[:n])
		s = s[n:]
	}
	return out
}
